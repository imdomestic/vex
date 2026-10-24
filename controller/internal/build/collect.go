package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/imdomestic/vex/controller/internal/manifest"
)

func selectedPath(name string, selection manifest.File) (bool, error) {
	matched, err := doublestar.Match(selection.Glob, name)
	if err != nil || !matched {
		return false, err
	}
	for _, exclude := range selection.Exclude {
		target := name
		if !strings.Contains(exclude, "/") {
			target = path.Base(name)
		}
		matched, err = doublestar.Match(exclude, target)
		if err != nil || matched {
			return false, err
		}
	}
	return true, nil
}

// CollectBundle 由构建 worker 调用，全部依赖下载和产物收集均在该进程完成。
func CollectBundle(ctx context.Context, doc manifest.Document, source, destination string, loaders []string) (Bundle, error) {
	bundle := Bundle{Schema: 1, Minecraft: doc.Minecraft.Version, Java: doc.Minecraft.Java, Loaders: map[string]BundleBuild{}}
	if err := manifest.Validate(doc, manifest.Policy{}); err != nil {
		return bundle, err
	}
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) {
		return bundle, fmt.Errorf("bundle collection requires absolute directories")
	}
	if err := os.Mkdir(destination, 0770); err != nil {
		return bundle, err
	}
	for _, loaderName := range loaders {
		loader, exists := doc.Loaders[loaderName]
		if !exists {
			return bundle, fmt.Errorf("selected loader is absent from manifest")
		}
		if _, duplicate := bundle.Loaders[loaderName]; duplicate {
			return bundle, fmt.Errorf("selected loader is repeated")
		}
		build := BundleBuild{LoaderVersion: loader.LoaderVersion, Files: []BundleFile{}}
		seen := map[string]bool{}
		add := func(filename, side, origin, sourceURL string) error {
			if err := regularSource(source, filename); err != nil && origin == "build" {
				return err
			}
			info, err := os.Lstat(filename)
			if err != nil {
				return err
			}
			name := filepath.Base(filename)
			if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > MaxFileBytes || !manifest.Filename(name) || !strings.HasSuffix(name, ".jar") || seen[strings.ToLower(name)] {
				return fmt.Errorf("artifact must be a unique regular bounded jar")
			}
			seen[strings.ToLower(name)] = true
			target := filepath.Join(destination, "files", loaderName, name)
			if err = os.MkdirAll(filepath.Dir(target), 0770); err != nil {
				return err
			}
			if err = copyFile(filename, target); err != nil {
				return err
			}
			file, err := describeFile(target, side)
			if err != nil {
				return err
			}
			if _, err = JarModIDs(target, loaderName); err != nil {
				return err
			}
			entry := bundleFile(file, loaderName, origin, sourceURL)
			build.Files = append(build.Files, entry)
			return nil
		}
		for _, selection := range loader.Files {
			matches, err := doublestar.Glob(os.DirFS(source), selection.Glob, doublestar.WithFailOnIOErrors(), doublestar.WithNoFollow())
			if err != nil {
				return bundle, err
			}
			count := 0
			for _, relative := range matches {
				selected, err := selectedPath(relative, selection)
				if err != nil {
					return bundle, err
				}
				if !selected {
					continue
				}
				if err = add(filepath.Join(source, filepath.FromSlash(relative)), selection.Side, "build", ""); err != nil {
					return bundle, err
				}
				count++
			}
			if count == 0 {
				return bundle, fmt.Errorf("manifest file selection matched no runtime artifacts")
			}
		}
		for _, dependency := range loader.Deps {
			parsed, err := url.Parse(dependency.URL)
			if err != nil {
				return bundle, err
			}
			directory, err := os.MkdirTemp(destination, ".dependency-")
			if err != nil {
				return bundle, err
			}
			filename := filepath.Join(directory, path.Base(parsed.Path))
			if err = downloadDependency(ctx, dependency, filename); err != nil {
				return bundle, err
			}
			if err = add(filename, dependency.Side, "dep", dependency.URL); err != nil {
				return bundle, err
			}
			if err = os.RemoveAll(directory); err != nil {
				return bundle, err
			}
		}
		bundle.Loaders[loaderName] = build
	}
	return bundle, bundle.validate()
}

func regularSource(root, filename string) error {
	relative, err := filepath.Rel(root, filename)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("artifact escapes source directory")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact path contains a symbolic link")
		}
	}
	return nil
}

func downloadDependency(ctx context.Context, dep manifest.Dependency, filename string) error {
	if !manifest.PublicURL(dep.URL) || !manifest.Digest(dep.SHA512, 128) {
		return fmt.Errorf("invalid dependency URL or checksum")
	}
	transport := &http.Transport{DialContext: publicDial, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !manifest.PublicURL(req.URL.String()) {
			return fmt.Errorf("invalid dependency redirect")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, dep.URL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("dependency request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > MaxFileBytes {
		return fmt.Errorf("dependency response status or size is invalid: HTTP %d", response.StatusCode)
	}
	output, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0660)
	if err != nil {
		return err
	}
	size, copyErr := io.Copy(output, io.LimitReader(response.Body, MaxFileBytes+1))
	if err = errors.Join(copyErr, output.Sync(), output.Close()); err != nil {
		return err
	}
	if size < 1 || size > MaxFileBytes {
		return fmt.Errorf("dependency exceeds its size limit")
	}
	file, err := describeFile(filename, dep.Side)
	if err != nil {
		return err
	}
	if file.SHA512 != dep.SHA512 || (dep.SHA1 != "" && dep.SHA1 != file.SHA1) || (dep.SHA256 != "" && dep.SHA256 != file.SHA256) {
		return fmt.Errorf("dependency checksum mismatch")
	}
	return nil
}

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, fmt.Errorf("dependency connection must use HTTPS port 443")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("dependency hostname lookup failed")
	}
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || netip.MustParsePrefix("100.64.0.0/10").Contains(address) {
			return nil, fmt.Errorf("dependency hostname resolves to a nonpublic address")
		}
	}
	dialer := net.Dialer{Timeout: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
}
