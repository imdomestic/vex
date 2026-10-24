package main

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const maxFileBytes = int64(256 * 1024 * 1024)
const maxBundleBytes = int64(2 * 1024 * 1024 * 1024)
const maxBundleFiles = 2048

type Bundle struct {
	Schema    int                     `json:"schema"`
	Minecraft string                  `json:"minecraft"`
	Java      int                     `json:"java"`
	Loaders   map[string]BundleLoader `json:"loaders"`
}

type BundleLoader struct {
	LoaderVersion string       `json:"loader_version"`
	Files         []BundleFile `json:"files"`
}

type BundleFile struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Side   string `json:"side"`
	Size   int64  `json:"size"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
	SHA512 string `json:"sha512"`
	Origin string `json:"origin"`
	URL    string `json:"url,omitempty"`
}

func collect(ctx context.Context, root string, doc Manifest, output string) (Bundle, error) {
	bundle := Bundle{Schema: 1, Minecraft: doc.Minecraft.Version, Java: doc.Minecraft.Java, Loaders: map[string]BundleLoader{}}
	if err := securePath(root, output, true); err != nil {
		return bundle, err
	}
	destination := filepath.Join(root, filepath.FromSlash(output))
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return bundle, err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return bundle, fmt.Errorf("bundle destination must be new: %w", err)
	}
	loaders := make([]string, 0, len(doc.Loaders))
	for name := range doc.Loaders {
		loaders = append(loaders, name)
	}
	sort.Strings(loaders)
	total, count := int64(0), 0
	for _, name := range loaders {
		loader := doc.Loaders[name]
		result := BundleLoader{LoaderVersion: loader.LoaderVersion, Files: []BundleFile{}}
		seen := map[string]bool{}
		add := func(source, side, origin, sourceURL string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			base := filepath.Base(source)
			if !filename(base) || !strings.HasSuffix(base, ".jar") || seen[strings.ToLower(base)] {
				return fmt.Errorf("artifact filename is unsafe or repeated")
			}
			info, err := os.Lstat(source)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxFileBytes || count >= maxBundleFiles || info.Size() > maxBundleBytes-total {
				return fmt.Errorf("artifact is not a regular bounded file")
			}
			nameInBundle := path.Join("files", name, base)
			target := filepath.Join(destination, filepath.FromSlash(nameInBundle))
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			input, err := os.Open(source)
			if err != nil {
				return err
			}
			file, err := copyDescribed(input, target)
			if err = errors.Join(err, input.Close()); err != nil {
				return err
			}
			if file.Size != info.Size() {
				return fmt.Errorf("artifact changed during collection")
			}
			file.Name, file.Path, file.Side, file.Origin, file.URL = base, nameInBundle, side, origin, sourceURL
			result.Files = append(result.Files, file)
			seen[strings.ToLower(base)] = true
			total += file.Size
			count++
			return nil
		}
		for _, selection := range loader.Files {
			matches, err := doublestar.Glob(os.DirFS(root), selection.Glob, doublestar.WithFailOnIOErrors(), doublestar.WithNoFollow())
			if err != nil {
				return bundle, err
			}
			matched := 0
			for _, relative := range matches {
				include, err := selected(relative, selection)
				if err != nil {
					return bundle, err
				}
				if !include {
					continue
				}
				if err = securePath(root, relative, false); err != nil {
					return bundle, err
				}
				if err = add(filepath.Join(root, filepath.FromSlash(relative)), selection.Side, "build", ""); err != nil {
					return bundle, err
				}
				matched++
			}
			if matched == 0 {
				return bundle, fmt.Errorf("manifest file selection matched no artifacts")
			}
		}
		for _, dep := range loader.Deps {
			parsed, err := url.Parse(dep.URL)
			if err != nil {
				return bundle, err
			}
			directory, err := os.MkdirTemp(destination, ".dependency-")
			if err != nil {
				return bundle, err
			}
			source := filepath.Join(directory, path.Base(parsed.Path))
			if err = download(ctx, dep, source); err != nil {
				return bundle, err
			}
			if err = add(source, dep.Side, "dep", dep.URL); err != nil {
				return bundle, err
			}
			if err = os.RemoveAll(directory); err != nil {
				return bundle, err
			}
		}
		bundle.Loaders[name] = result
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return bundle, err
	}
	if err = os.WriteFile(filepath.Join(destination, "vex-build.json"), append(data, '\n'), 0600); err != nil {
		return bundle, err
	}
	return bundle, nil
}

func securePath(root, relative string, allowMissing bool) error {
	if !filepath.IsAbs(root) || !fs.ValidPath(relative) || relative == "." || strings.ContainsAny(relative, "\\\x00\r\n") {
		return fmt.Errorf("paths must remain inside the checked out repository")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("repository root must be a regular directory")
	}
	current := root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path contains a symbolic link")
		}
	}
	return nil
}

func copyDescribed(input io.Reader, target string) (BundleFile, error) {
	var result BundleFile
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	first, second, third := sha1.New(), sha256.New(), sha512.New()
	size, copyErr := io.Copy(io.MultiWriter(output, first, second, third), io.LimitReader(input, maxFileBytes+1))
	if err = errors.Join(copyErr, output.Sync(), output.Close()); err != nil {
		return result, err
	}
	if size < 1 || size > maxFileBytes {
		return result, fmt.Errorf("file exceeds its size limit or is empty")
	}
	result.Size = size
	result.SHA1 = hex.EncodeToString(first.Sum(nil))
	result.SHA256 = hex.EncodeToString(second.Sum(nil))
	result.SHA512 = hex.EncodeToString(third.Sum(nil))
	return result, nil
}

func download(ctx context.Context, dep Dependency, target string) error {
	transport := &http.Transport{DialContext: publicDial, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !publicURL(req.URL.String()) {
			return fmt.Errorf("dependency redirect is unsafe")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, dep.URL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("dependency HTTPS request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > maxFileBytes {
		return fmt.Errorf("dependency response status or size is invalid: HTTP %d", response.StatusCode)
	}
	file, err := copyDescribed(response.Body, target)
	if err != nil {
		return err
	}
	if file.SHA1 != dep.SHA1 || file.SHA512 != dep.SHA512 || (dep.SHA256 != "" && file.SHA256 != dep.SHA256) {
		return fmt.Errorf("dependency checksum mismatch")
	}
	return nil
}

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, fmt.Errorf("dependency connection requires HTTPS port 443")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("dependency DNS lookup failed")
	}
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || netip.MustParsePrefix("100.64.0.0/10").Contains(address) {
			return nil, fmt.Errorf("dependency address is not public")
		}
	}
	dialer := net.Dialer{Timeout: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
}
