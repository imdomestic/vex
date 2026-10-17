package build

import (
	"archive/zip"
	"bytes"
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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/magiconair/properties"
	"github.com/pelletier/go-toml/v2"
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
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		root, err := filepath.EvalSymlinks(worktree)
		if err != nil {
			return "", err
		}
		resolvedRel, err := filepath.Rel(root, resolved)
		if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("artifact resolves outside the worktree")
		}
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
	file, err := describeFile(path, "")
	return file.SHA256, file.Size, err
}

func describeFile(path, side string) (model.BuildFile, error) {
	var result model.BuildFile
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	one, two, five := sha1.New(), sha256.New(), sha512.New()
	size, err := io.Copy(io.MultiWriter(one, two, five), file)
	if err != nil {
		return result, err
	}
	return model.BuildFile{
		Name: filepath.Base(path), Side: side, Size: size,
		SHA1: hex.EncodeToString(one.Sum(nil)), SHA256: hex.EncodeToString(two.Sum(nil)), SHA512: hex.EncodeToString(five.Sum(nil)),
	}, nil
}

func (m *Manager) buildFile(path string, value model.Build, side string) (model.BuildFile, error) {
	file, err := describeFile(path, side)
	if err != nil {
		return file, err
	}
	file.Path = filepath.ToSlash(filepath.Join("builds", value.ID, fmt.Sprintf("attempt-%d", value.Attempt), file.Name))
	file.URL = m.apiPrefix() + "/builds/" + value.ID + "/files/" + url.PathEscape(file.Name)
	return file, nil
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

func (m *Manager) dependency(ctx context.Context, props *properties.Properties, dep config.Dependency, loader string) (model.BuildFile, string, error) {
	var result model.BuildFile
	if dep.Side != "both" && dep.Side != "server" && dep.Side != "client" {
		return result, "", fmt.Errorf("invalid dependency side %q", dep.Side)
	}
	if _, _, err := props.Set("__vex_dependency_url", dep.URL); err != nil {
		return result, "", err
	}
	expanded, ok := props.Get("__vex_dependency_url")
	if !ok || strings.Contains(expanded, "${") {
		return result, "", fmt.Errorf("dependency URL contains an unresolved property")
	}
	u, err := url.Parse(expanded)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
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
		cached := filepath.Join(m.cfg.DataDir, "deps", dep.SHA256+".jar")
		if _, err = os.Stat(cached); err == nil {
			digest, size, err := hashFile(cached)
			if err != nil {
				return result, "", err
			}
			if digest != dep.SHA256 {
				return result, "", fmt.Errorf("cached dependency checksum mismatch")
			}
			return model.BuildFile{Name: name, SHA256: digest, Size: size, Side: dep.Side}, cached, nil
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
	staging, err := os.CreateTemp(filepath.Join(m.cfg.DataDir, ".work"), "dependency-*.jar")
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
	if _, err = JarModIDs(staging.Name(), loader); err != nil {
		return result, "", err
	}
	target := filepath.Join(m.cfg.DataDir, "deps", digest+".jar")
	if err = os.Rename(staging.Name(), target); err != nil {
		return result, "", err
	}
	return model.BuildFile{Name: name, SHA256: digest, Size: size, Side: dep.Side}, target, nil
}

func (m *Manager) agentArtifact(value model.Build) (*model.BuildFile, string, error) {
	loader := m.cfg.Loaders[value.Loader]
	if loader.Agent == "" {
		return nil, "", nil
	}
	agent, ok := m.cfg.Agents[loader.Agent]
	if !ok || value.Minecraft == nil || agent.Minecraft != *value.Minecraft || agent.Loader != value.Loader {
		return nil, "", fmt.Errorf("configured agent does not match this build")
	}
	info, err := os.Lstat(agent.File)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || filepath.Ext(agent.File) != ".jar" {
		return nil, "", fmt.Errorf("configured agent must be a regular jar")
	}
	ids, err := JarModIDs(agent.File, value.Loader)
	if err != nil {
		return nil, "", err
	}
	found := false
	for _, id := range ids {
		if id == "vex_agent" {
			found = true
		}
	}
	if !found {
		return nil, "", fmt.Errorf("agent artifact does not contain vex_agent")
	}
	file, err := m.buildFile(agent.File, value, "server")
	if err != nil {
		return nil, "", err
	}
	return &file, agent.File, nil
}

func JarModIDs(path, loader string) ([]string, error) {
	if loader != "fabric" && loader != "neoforge" {
		return nil, fmt.Errorf("unsupported JAR metadata loader")
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	ids := map[string]bool{}
	if err = collectModIDs(&reader.Reader, loader, ids, 0, true); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func collectModIDs(reader *zip.Reader, loader string, ids map[string]bool, depth int, required bool) error {
	if depth > 8 {
		return fmt.Errorf("nested jar depth exceeds eight")
	}
	var nested []string
	if loader == "fabric" {
		data, found, err := jarMetadata(reader, "fabric.mod.json")
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("Fabric JAR requires fabric.mod.json")
		}
		var mod struct {
			ID   string `json:"id"`
			Jars []struct {
				File string `json:"file"`
			} `json:"jars"`
		}
		if err = json.Unmarshal(data, &mod); err != nil {
			return err
		}
		if mod.ID == "" {
			return fmt.Errorf("Fabric metadata requires a mod ID")
		}
		ids[mod.ID] = true
		for _, entry := range mod.Jars {
			nested = append(nested, entry.File)
		}
	} else {
		data, found, err := jarMetadata(reader, "META-INF/neoforge.mods.toml")
		if err != nil {
			return err
		}
		if !found {
			data, found, err = jarMetadata(reader, "META-INF/mods.toml")
			if err != nil {
				return err
			}
		}
		if !found && required {
			return fmt.Errorf("NeoForge JAR requires mod metadata TOML")
		}
		if found {
			var metadata struct {
				Mods []struct {
					ID string `toml:"modId"`
				} `toml:"mods"`
			}
			if err = toml.Unmarshal(data, &metadata); err != nil {
				return err
			}
			if len(metadata.Mods) == 0 {
				return fmt.Errorf("NeoForge metadata requires at least one mod")
			}
			for _, mod := range metadata.Mods {
				if mod.ID == "" {
					return fmt.Errorf("NeoForge metadata requires modId")
				}
				ids[mod.ID] = true
			}
		}
		data, found, err = jarMetadata(reader, "META-INF/jarjar/metadata.json")
		if err != nil {
			return err
		}
		if found {
			var metadata struct {
				Jars []struct {
					Path string `json:"path"`
				} `json:"jars"`
			}
			if err = json.Unmarshal(data, &metadata); err != nil {
				return err
			}
			for _, entry := range metadata.Jars {
				nested = append(nested, entry.Path)
			}
		}
	}
	for _, name := range nested {
		if !fs.ValidPath(name) || strings.Contains(name, "\\") || !strings.HasSuffix(name, ".jar") {
			return fmt.Errorf("invalid nested JAR path")
		}
		file, err := reader.Open(name)
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
		if err = collectModIDs(nestedReader, loader, ids, depth+1, loader == "fabric"); err != nil {
			return err
		}
	}
	return nil
}

func jarMetadata(reader *zip.Reader, name string) ([]byte, bool, error) {
	file, err := reader.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err = errors.Join(readErr, file.Close()); err != nil {
		return nil, false, err
	}
	if len(data) > 1024*1024 {
		return nil, false, fmt.Errorf("JAR metadata exceeds 1 MiB")
	}
	return data, true, nil
}
