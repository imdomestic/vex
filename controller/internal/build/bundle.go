package build

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"golang.org/x/sys/unix"
)

const (
	MaxBundleBytes = int64(2 * 1024 * 1024 * 1024)
	MaxFileBytes   = int64(256 * 1024 * 1024)
	MaxBundleFiles = 2048
	MaxBundleJSON  = int64(4 * 1024 * 1024)
)

type Bundle struct {
	Schema    int                    `json:"schema"`
	Minecraft string                 `json:"minecraft"`
	Java      int                    `json:"java"`
	Loaders   map[string]BundleBuild `json:"loaders"`
}

type BundleBuild struct {
	Loader        string       `json:"-"`
	LoaderVersion string       `json:"loader_version"`
	Files         []BundleFile `json:"files"`
}

type BundleFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Side      string `json:"side"`
	Origin    string `json:"origin"`
	SourceURL string `json:"url,omitempty"`
	Size      int64  `json:"size"`
	SHA1      string `json:"sha1"`
	SHA256    string `json:"sha256"`
	SHA512    string `json:"sha512"`
}

func (bundle Bundle) validate() error {
	if bundle.Schema != 1 || !manifest.Version(bundle.Minecraft) || bundle.Java < 8 || bundle.Java > 99 || len(bundle.Loaders) == 0 || len(bundle.Loaders) > 2 {
		return fmt.Errorf("bundle schema, target or loader count is invalid")
	}
	total, count := int64(0), 0
	for _, build := range bundle.LoaderBuilds() {
		if (build.Loader != "fabric" && build.Loader != "neoforge") || !manifest.Version(build.LoaderVersion) || len(build.Files) == 0 {
			return fmt.Errorf("bundle has an unsupported or empty loader")
		}
		names := map[string]bool{}
		projectFiles := 0
		for _, file := range build.Files {
			if !manifest.Filename(file.Name) || !strings.HasSuffix(file.Name, ".jar") || names[strings.ToLower(file.Name)] || !manifest.Side(file.Side) || file.Path != path.Join("files", build.Loader, file.Name) {
				return fmt.Errorf("bundle file name, path or side is invalid")
			}
			names[strings.ToLower(file.Name)] = true
			if file.Size < 1 || file.Size > MaxFileBytes || !manifest.Digest(file.SHA1, 40) || !manifest.Digest(file.SHA256, 64) || !manifest.Digest(file.SHA512, 128) {
				return fmt.Errorf("bundle file size or checksum is invalid")
			}
			switch file.Origin {
			case "build":
				if file.SourceURL != "" {
					return fmt.Errorf("build artifacts cannot supply source URLs")
				}
				projectFiles++
			case "dep":
				if !manifest.PublicURL(file.SourceURL) {
					return fmt.Errorf("dependency source must be a public HTTPS jar URL")
				}
			default:
				return fmt.Errorf("bundle origin must be build or dep")
			}
			count++
			total += file.Size
			if count > MaxBundleFiles || total > MaxBundleBytes {
				return fmt.Errorf("bundle file count or total size exceeds its limit")
			}
		}
		if projectFiles == 0 {
			return fmt.Errorf("bundle must contain build artifacts")
		}
	}
	return nil
}

func (bundle Bundle) LoaderBuilds() []BundleBuild {
	names := make([]string, 0, len(bundle.Loaders))
	for name := range bundle.Loaders {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]BundleBuild, 0, len(names))
	for _, name := range names {
		selected := bundle.Loaders[name]
		selected.Loader = name
		result = append(result, selected)
	}
	return result
}

func (bundle Bundle) validateManifest(doc manifest.Document, allLoaders bool) error {
	if bundle.Minecraft != doc.Minecraft.Version || bundle.Java != doc.Minecraft.Java || (allLoaders && len(bundle.Loaders) != len(doc.Loaders)) {
		return fmt.Errorf("bundle target or loader count differs from the commit manifest")
	}
	for _, build := range bundle.LoaderBuilds() {
		loader, exists := doc.Loaders[build.Loader]
		if !exists || build.LoaderVersion != loader.LoaderVersion {
			return fmt.Errorf("bundle loader version differs from the commit manifest")
		}
		deps := map[string]manifest.Dependency{}
		for _, dependency := range loader.Deps {
			if _, exists := deps[dependency.URL]; exists {
				return fmt.Errorf("manifest repeats a dependency URL")
			}
			deps[dependency.URL] = dependency
		}
		used := map[string]bool{}
		for _, file := range build.Files {
			if file.Origin == "dep" {
				dependency, exists := deps[file.SourceURL]
				if !exists || used[file.SourceURL] || dependency.Side != file.Side || dependency.SHA1 != file.SHA1 || dependency.SHA512 != file.SHA512 || (dependency.SHA256 != "" && dependency.SHA256 != file.SHA256) {
					return fmt.Errorf("bundle dependency differs from the commit manifest")
				}
				used[file.SourceURL] = true
				continue
			}
			allowedSide := false
			for _, selection := range loader.Files {
				allowedSide = allowedSide || selection.Side == file.Side
			}
			if !allowedSide {
				return fmt.Errorf("bundle build side differs from the commit manifest")
			}
		}
		if len(used) != len(deps) {
			return fmt.Errorf("bundle must include every declared dependency")
		}
	}
	return nil
}

// UnpackBundle 仅读取 ZIP 内的文件，不解析或下载来源 URL。
func UnpackBundle(filename, destination string) (Bundle, error) {
	var result Bundle
	info, err := os.Lstat(filename)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBundleBytes || info.Size() < 1 {
		return result, fmt.Errorf("bundle must be a regular ZIP no larger than 2 GiB")
	}
	reader, err := zip.OpenReader(filename)
	if err != nil {
		return result, fmt.Errorf("open bundle ZIP: %w", err)
	}
	defer reader.Close()
	entries := map[string]*zip.File{}
	seen := map[string]bool{}
	total := uint64(0)
	if len(reader.File) > MaxBundleFiles+16 {
		return result, fmt.Errorf("ZIP entry count exceeds its limit")
	}
	for _, entry := range reader.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if !fs.ValidPath(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") || seen[strings.ToLower(name)] || entry.Flags&1 != 0 {
			return result, fmt.Errorf("ZIP contains an unsafe, encrypted or duplicate path")
		}
		seen[strings.ToLower(name)] = true
		if entry.FileInfo().IsDir() {
			if entry.Name != name+"/" || entry.UncompressedSize64 != 0 || (name != "files" && name != "files/fabric" && name != "files/neoforge") {
				return result, fmt.Errorf("ZIP contains an unexpected directory")
			}
			continue
		}
		if !entry.Mode().IsRegular() || entry.UncompressedSize64 > uint64(MaxFileBytes) || entry.UncompressedSize64 > uint64(MaxBundleBytes)-total {
			return result, fmt.Errorf("ZIP contains a special file or exceeds its size limit")
		}
		total += entry.UncompressedSize64
		entries[name] = entry
	}
	metadata, ok := entries["vex-build.json"]
	if !ok || metadata.UncompressedSize64 > uint64(MaxBundleJSON) {
		return result, fmt.Errorf("ZIP requires bounded vex-build.json metadata")
	}
	data, err := readZIPEntry(metadata, MaxBundleJSON)
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("invalid bundle JSON: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return result, fmt.Errorf("bundle JSON contains trailing data")
	}
	if err = result.validate(); err != nil {
		return result, err
	}
	wanted := map[string]BundleFile{}
	for _, build := range result.LoaderBuilds() {
		for _, file := range build.Files {
			wanted[file.Path] = file
		}
	}
	if len(entries) != len(wanted)+1 {
		return result, fmt.Errorf("ZIP contains missing or unlisted files")
	}
	var filesystem unix.Statfs_t
	if err = unix.Statfs(filepath.Dir(destination), &filesystem); err != nil {
		return result, err
	}
	if uint64(filesystem.Bavail)*uint64(filesystem.Bsize) < 15*1024*1024*1024+total {
		return result, fmt.Errorf("bundle extraction requires 15 GiB free space plus its uncompressed size")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return result, fmt.Errorf("bundle destination must be a new directory: %w", err)
	}
	for name, file := range wanted {
		entry, exists := entries[name]
		if !exists || entry.UncompressedSize64 != uint64(file.Size) {
			return result, fmt.Errorf("ZIP file is absent or has an incorrect size")
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return result, err
		}
		input, err := entry.Open()
		if err != nil {
			return result, err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			input.Close()
			return result, err
		}
		size, copyErr := io.Copy(output, io.LimitReader(input, file.Size+1))
		if err = errors.Join(copyErr, input.Close(), output.Sync(), output.Close()); err != nil {
			return result, err
		}
		actual, err := describeFile(target, file.Side)
		if err != nil {
			return result, err
		}
		if size != file.Size || actual.SHA1 != file.SHA1 || actual.SHA256 != file.SHA256 || actual.SHA512 != file.SHA512 {
			return result, fmt.Errorf("bundle file checksum or size mismatch")
		}
	}
	return result, nil
}

func readZIPEntry(entry *zip.File, limit int64) ([]byte, error) {
	input, err := entry.Open()
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(input, limit+1))
	if err = errors.Join(readErr, input.Close()); err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("ZIP entry exceeds its size limit")
	}
	return data, nil
}

func WriteBundleZIP(bundle Bundle, directory, filename string) (resultErr error) {
	if err := bundle.validate(); err != nil {
		return err
	}
	output, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0660)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(output)
	defer func() { resultErr = errors.Join(resultErr, writer.Close(), output.Sync(), output.Close()) }()
	metadata, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	entry, err := writer.Create("vex-build.json")
	if err != nil {
		return err
	}
	if _, err = entry.Write(metadata); err != nil {
		return err
	}
	files := []string{}
	for _, build := range bundle.LoaderBuilds() {
		for _, file := range build.Files {
			files = append(files, file.Path)
		}
	}
	sort.Strings(files)
	for _, name := range files {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return err
		}
		input, err := os.Open(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, input)
		if err = errors.Join(copyErr, input.Close()); err != nil {
			return err
		}
	}
	return nil
}

func bundleFile(file model.BuildFile, loader, origin, source string) BundleFile {
	return BundleFile{
		Path: path.Join("files", loader, file.Name), Name: file.Name, Side: file.Side,
		Origin: origin, SourceURL: source, Size: file.Size, SHA1: file.SHA1, SHA256: file.SHA256, SHA512: file.SHA512,
	}
}
