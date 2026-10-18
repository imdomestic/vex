package manifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/dustin/go-humanize"
	"github.com/pelletier/go-toml/v2"
)

const MaxBytes int64 = 1024 * 1024

type Document struct {
	Schema    int               `toml:"schema" json:"schema"`
	Minecraft Minecraft         `toml:"minecraft" json:"minecraft"`
	Loaders   map[string]Loader `toml:"loaders" json:"loaders"`
	World     World             `toml:"world" json:"world"`
	Rooms     Rooms             `toml:"rooms" json:"rooms"`
}

type Minecraft struct {
	Version string `toml:"version" json:"version"`
	Java    int    `toml:"java" json:"java"`
}

type Loader struct {
	LoaderVersion string       `toml:"loader_version" json:"loaderVersion"`
	Build         []string     `toml:"build" json:"build"`
	Agent         bool         `toml:"agent" json:"agent"`
	Files         []File       `toml:"files" json:"files"`
	Deps          []Dependency `toml:"deps" json:"deps"`
}

type File struct {
	Glob    string   `toml:"glob" json:"glob"`
	Exclude []string `toml:"exclude" json:"exclude"`
	Side    string   `toml:"side" json:"side"`
}

type Dependency struct {
	URL    string `toml:"url" json:"url"`
	SHA1   string `toml:"sha1" json:"sha1"`
	SHA256 string `toml:"sha256" json:"sha256"`
	SHA512 string `toml:"sha512" json:"sha512"`
	Side   string `toml:"side" json:"side"`
}

type World struct {
	ExtraRootEntries []string `toml:"extra_root_entries" json:"extraRootEntries"`
}

type Rooms struct {
	Memory string `toml:"memory" json:"memory"`
}

type Policy struct {
	Minecraft        string
	Java             map[int]bool
	Loaders          map[string]bool
	MaxMemory        string
	ExtraRootEntries []string
}

type FileReader interface {
	ReadFile(context.Context, string, string, int64) ([]byte, error)
}

type Provenance struct {
	Commit   string
	Fallback bool
}

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,127}$`)
var memoryPattern = regexp.MustCompile(`^[1-9][0-9]*[MG]$`)
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,199}$`)

func Version(value string) bool { return versionPattern.MatchString(value) }

func Side(value string) bool { return value == "client" || value == "server" || value == "both" }

func Filename(value string) bool {
	return namePattern.MatchString(value) && value != "." && value != ".." && !strings.HasSuffix(value, ".")
}

func Digest(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func Parse(data []byte) (Document, error) {
	var result Document
	if int64(len(data)) > MaxBytes {
		return result, fmt.Errorf("vex.toml exceeds 1 MiB")
	}
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("invalid vex.toml: %w", err)
	}
	return result, Validate(result, Policy{})
}

func ReadCommit(ctx context.Context, reader FileReader, sha, fallback string) (Document, Provenance, error) {
	provenance := Provenance{Commit: sha}
	data, err := reader.ReadFile(ctx, sha, "vex.toml", MaxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		if fallback == "" || !filepath.IsAbs(fallback) {
			return Document{}, provenance, fmt.Errorf("commit has no vex.toml and no absolute manifest_fallback is configured")
		}
		data, err = ReadLocal(fallback)
		provenance.Fallback = true
	}
	if err != nil {
		return Document{}, provenance, err
	}
	doc, err := Parse(data)
	return doc, provenance, err
}

func ReadLocal(filename string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return nil, fmt.Errorf("manifest must be a regular file no larger than 1 MiB")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err == nil && int64(len(data)) > MaxBytes {
		err = fmt.Errorf("manifest exceeds 1 MiB")
	}
	return data, err
}

func Validate(doc Document, policy Policy) error {
	if doc.Schema != 1 || !Version(doc.Minecraft.Version) || doc.Minecraft.Java < 8 || doc.Minecraft.Java > 99 {
		return fmt.Errorf("manifest requires schema 1, a Minecraft version and Java 8..99")
	}
	if policy.Minecraft != "" && doc.Minecraft.Version != policy.Minecraft {
		return fmt.Errorf("manifest Minecraft version is outside project policy")
	}
	if policy.Java != nil && !policy.Java[doc.Minecraft.Java] {
		return fmt.Errorf("manifest Java runtime is unavailable")
	}
	if len(doc.Loaders) == 0 || len(doc.Loaders) > 2 {
		return fmt.Errorf("manifest requires one or two supported loaders")
	}
	for name, loader := range doc.Loaders {
		if (name != "fabric" && name != "neoforge") || !Version(loader.LoaderVersion) || (policy.Loaders != nil && !policy.Loaders[name]) {
			return fmt.Errorf("manifest loader %q is unsupported or disabled", name)
		}
		if len(loader.Build) > 128 || len(loader.Files) == 0 || len(loader.Files) > 128 || len(loader.Deps) > 512 {
			return fmt.Errorf("manifest loader %s has invalid command or file counts", name)
		}
		for _, arg := range loader.Build {
			if arg == "" || len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") {
				return fmt.Errorf("manifest build command contains an invalid argument")
			}
		}
		for _, file := range loader.Files {
			if !safeGlob(file.Glob) || !Side(file.Side) || len(file.Exclude) > 128 {
				return fmt.Errorf("manifest contains an invalid file glob or side")
			}
			for _, exclude := range file.Exclude {
				if !safeGlob(exclude) {
					return fmt.Errorf("manifest contains an invalid exclusion glob")
				}
			}
		}
		for _, dep := range loader.Deps {
			if !PublicURL(dep.URL) || !Side(dep.Side) || !Digest(dep.SHA1, 40) || !Digest(dep.SHA512, 128) {
				return fmt.Errorf("manifest dependency requires a public HTTPS jar URL, side, SHA-1 and SHA-512")
			}
			if (dep.SHA1 != "" && !Digest(dep.SHA1, 40)) || (dep.SHA256 != "" && !Digest(dep.SHA256, 64)) {
				return fmt.Errorf("manifest dependency checksum is invalid")
			}
		}
	}
	if err := ValidateRootEntries(doc.World.ExtraRootEntries, policy.ExtraRootEntries); err != nil {
		return err
	}
	return ValidateMemory(doc.Rooms.Memory, policy.MaxMemory)
}

func ValidateRootEntries(entries, permitted []string) error {
	allowed := map[string]bool{}
	for _, entry := range permitted {
		allowed[entry] = true
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !Filename(entry) || seen[strings.ToLower(entry)] || (permitted != nil && !allowed[entry]) {
			return fmt.Errorf("manifest world root entry is invalid or disallowed")
		}
		seen[strings.ToLower(entry)] = true
	}
	return nil
}

func ValidateMemory(memory, maximum string) error {
	if memory != "" {
		if !memoryPattern.MatchString(memory) {
			return fmt.Errorf("manifest room memory must use a positive M or G quantity")
		}
		requested, err := humanize.ParseBytes(memory + "iB")
		if err != nil || requested == 0 {
			return fmt.Errorf("manifest room memory is invalid")
		}
		if maximum != "" {
			limit, err := humanize.ParseBytes(maximum)
			if err != nil || limit == 0 || requested > limit {
				return fmt.Errorf("manifest room memory exceeds project quota")
			}
		}
	}
	return nil
}

func PublicURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Port() == "" || u.Port() == "443") && Filename(path.Base(u.Path)) && strings.HasSuffix(u.Path, ".jar")
}

func safeGlob(value string) bool {
	if value == "" || len(value) > 1024 || strings.ContainsAny(value, "\\\x00\r\n():") || path.IsAbs(value) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	_, err := doublestar.Match(value, "")
	return err == nil
}

func (doc Document) LoaderNames() []string {
	result := make([]string, 0, len(doc.Loaders))
	for name := range doc.Loaders {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
