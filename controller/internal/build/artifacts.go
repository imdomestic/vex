package build

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/magiconair/properties"
)

func artifactPath(worktree string, loader config.Loader) (string, error) {
	if filepath.IsAbs(loader.ArtifactGlob) {
		return "", fmt.Errorf("artifact glob must be relative")
	}
	paths, err := filepath.Glob(filepath.Join(worktree, loader.ArtifactGlob))
	if err != nil {
		return "", err
	}
	selected := []string{}
	for _, path := range paths {
		rel, err := filepath.Rel(worktree, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("artifact escapes worktree")
		}
		excluded := false
		for _, pattern := range loader.ArtifactExclude {
			matches, err := filepath.Match(pattern, filepath.Base(path))
			if err != nil {
				return "", err
			}
			if matches {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || filepath.Ext(path) != ".jar" {
			return "", fmt.Errorf("artifact must be a regular jar: %s", rel)
		}
		selected = append(selected, path)
	}
	if len(selected) != 1 {
		return "", fmt.Errorf("expected one runtime artifact, found %d", len(selected))
	}
	return selected[0], nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (m *Manager) dependency(ctx context.Context, props *properties.Properties, dep config.Dependency) (model.BuildFile, string, error) {
	var result model.BuildFile
	if dep.Side != "both" && dep.Side != "server" && dep.Side != "client" {
		return result, "", fmt.Errorf("invalid dependency side %q", dep.Side)
	}
	if _, _, err := props.Set("__chorus_dependency_url", dep.URL); err != nil {
		return result, "", err
	}
	expanded, ok := props.Get("__chorus_dependency_url")
	if !ok || strings.Contains(expanded, "${") {
		return result, "", fmt.Errorf("dependency URL contains an unresolved property")
	}
	u, err := url.Parse(expanded)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return result, "", fmt.Errorf("dependency requires a public HTTPS URL")
	}
	name := filepath.Base(u.Path)
	if name == "." || name == "/" || !strings.HasSuffix(name, ".jar") {
		return result, "", fmt.Errorf("dependency URL must name a jar")
	}
	if dep.SHA256 != "" {
		if !digestPattern.MatchString(dep.SHA256) {
			return result, "", fmt.Errorf("invalid configured dependency SHA-256")
		}
		cached := filepath.Join(m.cfg.Server.DataDir, "deps", dep.SHA256+".jar")
		if _, err = os.Stat(cached); err == nil {
			digest, size, err := hashFile(cached)
			if err != nil {
				return result, "", err
			}
			if digest != dep.SHA256 {
				return result, "", fmt.Errorf("cached dependency checksum mismatch")
			}
			return model.BuildFile{Name: name, SHA256: digest, Size: size, Side: dep.Side, URL: "/v1/deps/" + digest}, cached, nil
		} else if !os.IsNotExist(err) {
			return result, "", err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, expanded, nil)
	if err != nil {
		return result, "", err
	}
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.User != nil {
			return fmt.Errorf("invalid dependency redirect")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return result, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, "", fmt.Errorf("download dependency: HTTP %d", response.StatusCode)
	}
	staging, err := os.CreateTemp(filepath.Join(m.cfg.Server.DataDir, ".work"), "dependency-*.jar")
	if err != nil {
		return result, "", err
	}
	defer os.Remove(staging.Name())
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(staging, hash), io.LimitReader(response.Body, 256*1024*1024+1))
	syncErr := staging.Sync()
	closeErr := staging.Close()
	if copyErr != nil {
		return result, "", copyErr
	}
	if syncErr != nil {
		return result, "", syncErr
	}
	if closeErr != nil {
		return result, "", closeErr
	}
	if size > 256*1024*1024 {
		return result, "", fmt.Errorf("dependency exceeds 256 MiB")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if dep.SHA256 != "" && digest != dep.SHA256 {
		return result, "", fmt.Errorf("dependency SHA-256 mismatch")
	}
	if _, err = JarModIDs(staging.Name()); err != nil {
		return result, "", err
	}
	target := filepath.Join(m.cfg.Server.DataDir, "deps", digest+".jar")
	if err = os.Rename(staging.Name(), target); err != nil {
		return result, "", err
	}
	return model.BuildFile{Name: name, SHA256: digest, Size: size, Side: dep.Side, URL: "/v1/deps/" + digest}, target, nil
}

func (m *Manager) hopArtifact(minecraft string) (*model.BuildFile, string, error) {
	if filepath.Base(minecraft) != minecraft || minecraft == "." || minecraft == ".." {
		return nil, "", fmt.Errorf("invalid Minecraft version path")
	}
	directory := filepath.Join(m.cfg.Server.DataDir, "mods", "chorus-hop", minecraft)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var selected string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".jar" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, "", err
		}
		if !info.Mode().IsRegular() {
			return nil, "", fmt.Errorf("hop artifact must be a regular jar")
		}
		if selected != "" {
			return nil, "", fmt.Errorf("Minecraft version requires exactly one configured hop artifact")
		}
		selected = filepath.Join(directory, entry.Name())
	}
	if selected == "" {
		return nil, "", fmt.Errorf("configured hop artifact directory contains no jar")
	}
	ids, err := JarModIDs(selected)
	if err != nil {
		return nil, "", err
	}
	found := false
	for _, id := range ids {
		if id == "chorus-hop" {
			found = true
		}
	}
	if !found {
		return nil, "", fmt.Errorf("hop artifact does not contain chorus-hop")
	}
	digest, size, err := hashFile(selected)
	if err != nil {
		return nil, "", err
	}
	published := filepath.Join(directory, "published", digest+".jar")
	if err = publishHop(selected, published, digest, size); err != nil {
		return nil, "", err
	}
	return &model.BuildFile{Name: filepath.Base(selected), SHA256: digest, Size: size, Side: "server", URL: "/v1/mods/chorus-hop/" + url.PathEscape(minecraft) + "/" + digest + ".jar"}, published, nil
}

func publishHop(source, target, expectedDigest string, expectedSize int64) error {
	info, err := os.Lstat(target)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("published hop artifact must be a regular jar")
		}
		digest, size, err := hashFile(target)
		if err != nil {
			return err
		}
		if digest != expectedDigest || size != expectedSize {
			return fmt.Errorf("published hop artifact checksum mismatch")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), ".publishing-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	path := filepath.Join(staging, "artifact.jar")
	if err = copyFile(source, path); err != nil {
		return err
	}
	digest, size, err := hashFile(path)
	if err != nil {
		return err
	}
	if digest != expectedDigest || size != expectedSize {
		return fmt.Errorf("configured hop artifact changed during publication")
	}
	if err = os.Chmod(path, 0440); err != nil {
		return err
	}
	if err = os.Link(path, target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func JarModIDs(path string) ([]string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	ids := map[string]bool{}
	if err = collectModIDs(&reader.Reader, ids, 0); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func collectModIDs(reader *zip.Reader, ids map[string]bool, depth int) error {
	if depth > 8 {
		return fmt.Errorf("nested jar depth exceeds eight")
	}
	metadata, err := reader.Open("fabric.mod.json")
	if err != nil {
		return fmt.Errorf("read Fabric metadata: %w", err)
	}
	var mod struct {
		ID   string `json:"id"`
		Jars []struct {
			File string `json:"file"`
		} `json:"jars"`
	}
	err = json.NewDecoder(io.LimitReader(metadata, 1024*1024)).Decode(&mod)
	metadata.Close()
	if err != nil {
		return err
	}
	if mod.ID == "" {
		return fmt.Errorf("Fabric metadata requires a mod ID")
	}
	ids[mod.ID] = true
	for _, nested := range mod.Jars {
		file, err := reader.Open(nested.File)
		if err != nil {
			return err
		}
		content, err := io.ReadAll(io.LimitReader(file, 256*1024*1024+1))
		file.Close()
		if err != nil {
			return err
		}
		if len(content) > 256*1024*1024 {
			return fmt.Errorf("nested jar exceeds 256 MiB")
		}
		nestedReader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		if err != nil {
			return err
		}
		if err = collectModIDs(nestedReader, ids, depth+1); err != nil {
			return err
		}
	}
	return nil
}
