package main

import (
	"bytes"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/pelletier/go-toml/v2"
)

type Manifest struct {
	Schema    int               `toml:"schema"`
	Minecraft Minecraft         `toml:"minecraft"`
	Loaders   map[string]Loader `toml:"loaders"`
	World     World             `toml:"world"`
	Rooms     Rooms             `toml:"rooms"`
}

type Minecraft struct {
	Version string `toml:"version"`
	Java    int    `toml:"java"`
}

type Loader struct {
	LoaderVersion string       `toml:"loader_version"`
	Build         []string     `toml:"build"`
	Agent         bool         `toml:"agent"`
	Files         []Selection  `toml:"files"`
	Deps          []Dependency `toml:"deps"`
}

type Selection struct {
	Glob    string   `toml:"glob"`
	Exclude []string `toml:"exclude"`
	Side    string   `toml:"side"`
}

type Dependency struct {
	URL    string `toml:"url"`
	SHA1   string `toml:"sha1"`
	SHA256 string `toml:"sha256"`
	SHA512 string `toml:"sha512"`
	Side   string `toml:"side"`
}

type World struct {
	ExtraRootEntries []string `toml:"extra_root_entries"`
}

type Rooms struct {
	Memory string `toml:"memory"`
}

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,127}$`)
var filenamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,199}$`)
var memoryPattern = regexp.MustCompile(`^[1-9][0-9]*[MG]$`)

func parseManifest(data []byte) (Manifest, error) {
	var result Manifest
	if len(data) > 1024*1024 {
		return result, fmt.Errorf("manifest exceeds 1 MiB")
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&result); err != nil {
		return result, err
	}
	if result.Schema != 1 || !versionPattern.MatchString(result.Minecraft.Version) || result.Minecraft.Java < 8 || result.Minecraft.Java > 99 || len(result.Loaders) == 0 || len(result.Loaders) > 2 {
		return result, fmt.Errorf("invalid manifest schema, target or loaders")
	}
	for name, loader := range result.Loaders {
		if (name != "fabric" && name != "neoforge") || !versionPattern.MatchString(loader.LoaderVersion) || len(loader.Files) == 0 || len(loader.Files) > 128 || len(loader.Deps) > 512 || len(loader.Build) > 128 {
			return result, fmt.Errorf("invalid loader declaration")
		}
		for _, arg := range loader.Build {
			if arg == "" || len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") {
				return result, fmt.Errorf("invalid build command argument")
			}
		}
		for _, selection := range loader.Files {
			if !safeGlob(selection.Glob) || !validSide(selection.Side) || len(selection.Exclude) > 128 {
				return result, fmt.Errorf("invalid file selection")
			}
			for _, exclude := range selection.Exclude {
				if !safeGlob(exclude) {
					return result, fmt.Errorf("invalid file exclusion")
				}
			}
		}
		seen := map[string]bool{}
		for _, dep := range loader.Deps {
			if !publicURL(dep.URL) || seen[dep.URL] || !validSide(dep.Side) || !digest(dep.SHA1, 40) || !digest(dep.SHA512, 128) || (dep.SHA256 != "" && !digest(dep.SHA256, 64)) {
				return result, fmt.Errorf("invalid or repeated dependency")
			}
			seen[dep.URL] = true
		}
	}
	seen := map[string]bool{}
	for _, entry := range result.World.ExtraRootEntries {
		if !filename(entry) || seen[strings.ToLower(entry)] {
			return result, fmt.Errorf("invalid or repeated world root entry")
		}
		seen[strings.ToLower(entry)] = true
	}
	if result.Rooms.Memory != "" && !memoryPattern.MatchString(result.Rooms.Memory) {
		return result, fmt.Errorf("invalid room memory")
	}
	return result, nil
}

func digest(value string, length int) bool {
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

func filename(value string) bool {
	return filenamePattern.MatchString(value) && !strings.HasSuffix(value, ".")
}

func validSide(value string) bool {
	return value == "both" || value == "client" || value == "server"
}

func publicURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Port() == "" || u.Port() == "443") && filename(path.Base(u.Path)) && strings.HasSuffix(u.Path, ".jar")
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

func selected(name string, selection Selection) (bool, error) {
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
