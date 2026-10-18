package mrpack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tnze/go-mc/nbt"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
)

const ContentType = "application/x-modrinth-modpack+zip"

type OpenFile func(context.Context, model.BuildFile) (*os.File, error)

type Index struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Files         []File            `json:"files"`
	Dependencies  map[string]string `json:"dependencies"`
}

type File struct {
	Path      string            `json:"path"`
	Hashes    map[string]string `json:"hashes"`
	Env       map[string]string `json:"env"`
	Downloads []string          `json:"downloads"`
	FileSize  int64             `json:"fileSize"`
}

type preparedFile struct {
	entry model.BuildFile
	file  *os.File
	path  string
}

type archiveEntry struct {
	path   string
	data   []byte
	source *preparedFile
}

var filenamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*\.jar$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

func Export(ctx context.Context, output io.Writer, build model.Build, room *model.Room, open OpenFile) error {
	if build.Status != "ready" || !config.ValidID(build.Project) || !versionPattern.MatchString(build.ID) || build.Minecraft == nil || build.LoaderVersion == nil || !versionPattern.MatchString(*build.Minecraft) || !versionPattern.MatchString(*build.LoaderVersion) || open == nil {
		return model.Failure("conflict", "mrpack requires a complete ready build")
	}
	loader := ""
	switch build.Loader {
	case "fabric":
		loader = "fabric-loader"
	case "neoforge":
		loader = "neoforge"
	default:
		return model.Failure("invalid_request", "mrpack loader is unsupported")
	}
	index := Index{FormatVersion: 1, Game: "minecraft", VersionID: fmt.Sprintf("%s-a%d", build.ID, build.Attempt), Name: build.Project + " - " + build.ID, Files: []File{}, Dependencies: map[string]string{"minecraft": *build.Minecraft, loader: *build.LoaderVersion}}
	if room != nil {
		if room.Project != build.Project || room.BuildID != build.ID || room.Generation < 0 || room.Generation == 0 && room.State != "stopped" || !versionPattern.MatchString(room.ID) || !validRoomHost(room.Host) || room.Port < 1 || room.Port > 65535 {
			return model.Failure("conflict", "room does not reference this build")
		}
		index.VersionID = fmt.Sprintf("%s-g%d-%s-a%d", room.ID, room.Generation, build.ID, build.Attempt)
		index.Name = build.Project + " - " + room.ID
	}
	files := append([]model.BuildFile(nil), build.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	prepared := []preparedFile{}
	defer func() {
		for _, entry := range prepared {
			entry.file.Close()
		}
	}()
	names := map[string]bool{}
	for _, entry := range files {
		if entry.Side == "server" {
			continue
		}
		if entry.Side != "client" && entry.Side != "both" || !filenamePattern.MatchString(entry.Name) || strings.Contains(entry.Name, "..") || entry.Size <= 0 || !validHash(entry.SHA1, 20) || !validHash(entry.SHA256, 32) || !validHash(entry.SHA512, 64) || names[strings.ToLower(entry.Name)] {
			return model.Failure("invalid_request", "build contains an invalid client file")
		}
		names[strings.ToLower(entry.Name)] = true
		file, err := open(ctx, entry)
		if err != nil {
			return err
		}
		prepared = append(prepared, preparedFile{entry: entry, file: file})
		if err = verify(ctx, file, entry); err != nil {
			return err
		}
		var download string
		if entry.Origin == "dep" && trustedCDN(entry.SourceURL) {
			download, err = verifiedDownload(ctx, entry)
			if err != nil {
				return err
			}
		}
		if download != "" {
			server := "required"
			if entry.Side == "client" {
				server = "unsupported"
			}
			index.Files = append(index.Files, File{Path: "mods/" + entry.Name, Hashes: map[string]string{"sha1": entry.SHA1, "sha512": entry.SHA512}, Env: map[string]string{"client": "required", "server": server}, Downloads: []string{download}, FileSize: entry.Size})
		} else {
			directory := "overrides"
			if entry.Side == "client" {
				directory = "client-overrides"
			}
			prepared[len(prepared)-1].path = directory + "/mods/" + entry.Name
		}
	}
	if len(prepared) == 0 {
		return model.Failure("invalid_request", "build has no client files")
	}
	manifest, err := json.Marshal(index)
	if err != nil {
		return err
	}
	entries := []archiveEntry{{path: "modrinth.index.json", data: append(manifest, '\n')}}
	for i := range prepared {
		if prepared[i].path != "" {
			entries = append(entries, archiveEntry{path: prepared[i].path, source: &prepared[i]})
		}
	}
	if room != nil {
		var servers bytes.Buffer
		if err = writeServers(&servers, index.Name, net.JoinHostPort(room.Host, strconv.Itoa(room.Port))); err != nil {
			return err
		}
		entries = append(entries, archiveEntry{path: "overrides/servers.dat", data: servers.Bytes()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	writer := zip.NewWriter(output)
	for _, item := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		header := &zip.FileHeader{Name: item.path, Method: zip.Deflate, Modified: time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)}
		header.SetMode(0644)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if item.source == nil {
			if _, err = entry.Write(item.data); err != nil {
				return err
			}
			continue
		}
		file := item.source
		if _, err = file.file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		hash := sha512.New()
		size, err := io.Copy(io.MultiWriter(entry, hash), &contextReader{ctx, file.file})
		if err != nil {
			return err
		}
		if size != file.entry.Size || hex.EncodeToString(hash.Sum(nil)) != file.entry.SHA512 {
			return fmt.Errorf("build file changed during mrpack export")
		}
	}
	return writer.Close()
}

func validHash(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size && value == strings.ToLower(value)
}

func validRoomHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || strings.ContainsAny(host, ":/\\@ \x00") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func verify(ctx context.Context, file *os.File, entry model.BuildFile) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != entry.Size {
		return fmt.Errorf("mrpack source file size or type differs from manifest")
	}
	sha1Hash, sha256Hash, sha512Hash := sha1.New(), sha256.New(), sha512.New()
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err = io.Copy(io.MultiWriter(sha1Hash, sha256Hash, sha512Hash), &contextReader{ctx, file}); err != nil {
		return err
	}
	if hex.EncodeToString(sha1Hash.Sum(nil)) != entry.SHA1 || hex.EncodeToString(sha256Hash.Sum(nil)) != entry.SHA256 || hex.EncodeToString(sha512Hash.Sum(nil)) != entry.SHA512 {
		return fmt.Errorf("mrpack source file digest differs from manifest")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(data)
}

func writeServers(output io.Writer, name, address string) error {
	data := struct {
		Servers []struct {
			Name string `nbt:"name"`
			IP   string `nbt:"ip"`
		} `nbt:"servers"`
	}{}
	data.Servers = append(data.Servers, struct {
		Name string `nbt:"name"`
		IP   string `nbt:"ip"`
	}{name, address})
	return nbt.NewEncoder(output).Encode(data, "")
}
