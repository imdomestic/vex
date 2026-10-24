// Package testfixture 提供 Vex 自己保存的固定测试文件。小文件位于本目录的 testdata，
// 大文件位于 encore 的固定目录（VEX_IT_FIXTURES，见 docs/development.md）。每个文件在使用前按 SHA-256 与大小校验。
package testfixture

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

//go:embed fixtures.json
var catalog []byte

// 仓库中的小文件嵌入测试程序，交叉编译的测试程序在其他主机上同样可用；首次使用时写入进程自己的临时目录。
//
//go:embed testdata
var repository embed.FS

var extracted = sync.OnceValues(func() (string, error) { return os.MkdirTemp("", "vex-fixtures-") })

type entry struct {
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Location string `json:"location"`
}

var verified sync.Map

// File 返回经过校验的固定测试文件路径；外部文件缺少 VEX_IT_FIXTURES 时跳过测试。
func File(t testing.TB, name string) string {
	t.Helper()
	var entries []entry
	if err := json.Unmarshal(catalog, &entries); err != nil {
		t.Fatal(err)
	}
	for _, item := range entries {
		if item.Name != name {
			continue
		}
		var filename string
		switch item.Location {
		case "repository":
			directory, err := extracted()
			if err != nil {
				t.Fatal(err)
			}
			filename = filepath.Join(directory, name)
			if _, ok := verified.Load(filename); !ok {
				data, err := repository.ReadFile("testdata/" + name)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filename, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		case "external":
			directory := os.Getenv("VEX_IT_FIXTURES")
			if directory == "" {
				t.Skip("requires VEX_IT_FIXTURES with the fixed Vex test files")
			}
			if !filepath.IsAbs(directory) {
				t.Fatal("VEX_IT_FIXTURES must be absolute")
			}
			filename = filepath.Join(directory, name)
		default:
			t.Fatalf("fixture %s has an unknown location", name)
		}
		if _, ok := verified.Load(filename); !ok {
			if err := check(filename, item); err != nil {
				t.Fatalf("fixture %s: %v", name, err)
			}
			verified.Store(filename, true)
		}
		return filename
	}
	t.Fatalf("fixture %s is not listed", name)
	return ""
}

func check(filename string, item entry) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return err
	}
	if size != item.Size || hex.EncodeToString(digest.Sum(nil)) != item.SHA256 {
		return errors.New("SHA-256 or size differs from fixtures.json")
	}
	return nil
}

// Copy 把固定文件复制到 destination，供测试在自己的目录中修改或发布。
func Copy(t testing.TB, name, destination string) string {
	t.Helper()
	source, err := os.Open(File(t, name))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, source)
	if err = errors.Join(copyErr, output.Close()); err != nil {
		t.Fatal(err)
	}
	return destination
}

// ResourcePack 按固定 Minecraft 版本数据中的资源包格式，在 destination 写入一个资源包。
func ResourcePack(t testing.TB, destination string) string {
	t.Helper()
	data, err := os.ReadFile(File(t, "minecraft-26.3-version.json"))
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		Pack struct {
			Major int `json:"resource_major"`
			Minor int `json:"resource_minor"`
		} `json:"pack_version"`
	}
	if err = json.Unmarshal(data, &version); err != nil || version.Pack.Major <= 0 {
		t.Fatal("fixed Minecraft version data has no resource pack format", err)
	}
	metadata, err := json.Marshal(map[string]any{"pack": map[string]any{"description": "Vex integration", "min_format": []int{version.Pack.Major, version.Pack.Minor}, "max_format": []int{version.Pack.Major, version.Pack.Minor}}})
	if err != nil {
		t.Fatal(err)
	}
	translation, err := json.Marshal(map[string]string{"vex.integration": "Vex integration"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"pack.mcmeta", metadata}, {"assets/vex/lang/en_us.json", translation}} {
		part, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err = errors.Join(writer.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	return destination
}

// WorldDirectory 把固定世界归档 name 解压到 destination，返回其中的 world 目录。
func WorldDirectory(t testing.TB, name, destination string) string {
	t.Helper()
	file, err := os.Open(File(t, name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsLocal(header.Name) {
			t.Fatalf("fixture archive contains a non-local path %q", header.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(header.Name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(target, 0700); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.Copy(output, reader)
			if err = errors.Join(copyErr, output.Close()); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("fixture archive contains an unsupported entry %q", header.Name)
		}
	}
	return filepath.Join(destination, "world")
}
