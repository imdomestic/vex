//go:build integration

package mrpack

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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tnze/go-mc/nbt"
	"github.com/imdomestic/vex/controller/internal/model"
)

func TestActualBuildExportsDeterministicRoomAndBuildPacks(t *testing.T) {
	manifestPath, dataDir, work := os.Getenv("VEX_IT_BUILD"), os.Getenv("VEX_IT_PROJECT_DATA"), os.Getenv("VEX_IT_WORK")
	if manifestPath == "" || dataDir == "" || work == "" {
		t.Skip("requires a real ready build and its actual project artifact directory")
	}
	if !filepath.IsAbs(work) || !strings.Contains(filepath.Clean(work), "/.work/") {
		t.Fatal("mrpack evidence must remain in an ignored workspace")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var build model.Build
	if err = json.Unmarshal(data, &build); err != nil {
		t.Fatal(err)
	}
	open := func(_ context.Context, entry model.BuildFile) (*os.File, error) {
		if !filepath.IsLocal(entry.Path) || entry.Path == "." {
			return nil, fmt.Errorf("artifact path is not project-relative")
		}
		return os.Open(filepath.Join(dataDir, filepath.FromSlash(entry.Path)))
	}
	room := model.Room{Project: build.Project, ID: "pr123-fabric", Kind: "pr", State: "stopped", BuildID: build.ID, Host: "pr123-fabric--" + build.Project + ".integration.vex.internal", Port: 25565}
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		name string
		room *model.Room
	}{{"build", nil}, {"room", &room}} {
		var first, second bytes.Buffer
		if err = Export(t.Context(), &first, build, target.room, open); err != nil {
			t.Fatal(err)
		}
		if err = Export(t.Context(), &second, build, target.room, open); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Fatal("identical actual build exports produced different bytes")
		}
		archive, err := zip.NewReader(bytes.NewReader(first.Bytes()), int64(first.Len()))
		if err != nil {
			t.Fatal(err)
		}
		entries := map[string][]byte{}
		paths := []string{}
		for _, entry := range archive.File {
			if !entry.Modified.Equal(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) {
				t.Fatal("archive entry has a varying timestamp")
			}
			stream, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			contents, err := io.ReadAll(stream)
			closeErr := stream.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			entries[entry.Name] = contents
			paths = append(paths, entry.Name)
		}
		if !slices.IsSorted(paths) {
			t.Fatal("archive entries are not sorted")
		}
		var index Index
		if err = json.Unmarshal(entries["modrinth.index.json"], &index); err != nil || index.FormatVersion != 1 || index.Game != "minecraft" || index.Dependencies["minecraft"] != *build.Minecraft || index.Dependencies["fabric-loader"] != *build.LoaderVersion {
			t.Fatal("actual build dependencies differ from mrpack index", err)
		}
		for _, file := range build.Files {
			if file.Side == "server" {
				for path := range entries {
					if strings.HasSuffix(path, "/"+file.Name) {
						t.Fatal("server-only artifact was exported")
					}
				}
				continue
			}
			directory := "overrides"
			if file.Side == "client" {
				directory = "client-overrides"
			}
			contents, embedded := entries[directory+"/mods/"+file.Name]
			if !embedded {
				t.Fatal("fixture without verified public CDN metadata was not embedded")
			}
			digest := sha256.Sum256(contents)
			if hex.EncodeToString(digest[:]) != file.SHA256 || int64(len(contents)) != file.Size {
				t.Fatal("embedded artifact differs from actual ready build")
			}
		}
		servers, present := entries["overrides/servers.dat"]
		if target.room == nil && present || target.room != nil && !present {
			t.Fatal("servers.dat presence differs from export target")
		}
		if present {
			var list struct {
				Servers []struct {
					IP string `nbt:"ip"`
				} `nbt:"servers"`
			}
			if _, err = nbt.NewDecoder(bytes.NewReader(servers)).Decode(&list); err != nil || len(list.Servers) != 1 || list.Servers[0].IP != room.Host+":25565" {
				t.Fatal("uncompressed servers.dat does not target the stopped PR", err)
			}
		}
		path := filepath.Join(work, "actual-"+target.name+".mrpack")
		if err = os.WriteFile(path, first.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(first.Bytes())
		t.Logf("actual %s pack: bytes=%d sha256=%x entries=%d path=%s", target.name, first.Len(), digest, len(paths), path)
	}
	var dependency *model.BuildFile
	for i := range build.Files {
		if strings.HasPrefix(build.Files[i].Name, "fabric-api-") {
			dependency = &build.Files[i]
			break
		}
	}
	if dependency == nil {
		t.Fatal("actual build fixture lacks a public Fabric API dependency")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.modrinth.com/v2/version_file/"+dependency.SHA512+"?algorithm=sha512", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("official hash metadata returned HTTP %d", response.StatusCode)
	}
	var version struct {
		Files []struct {
			URL    string            `json:"url"`
			Size   int64             `json:"size"`
			Hashes map[string]string `json:"hashes"`
		} `json:"files"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&version); err != nil {
		t.Fatal(err)
	}
	for _, file := range version.Files {
		if file.Hashes["sha512"] == dependency.SHA512 && file.Hashes["sha1"] == dependency.SHA1 && file.Size == dependency.Size && trustedCDN(file.URL) {
			dependency.Origin, dependency.SourceURL = "dep", file.URL
			break
		}
	}
	if dependency.SourceURL == "" {
		t.Fatal("official metadata did not verify the actual dependency bytes")
	}
	var published bytes.Buffer
	if err = Export(t.Context(), &published, build, nil, open); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(published.Bytes()), int64(published.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range archive.File {
		if file.Name == "overrides/mods/"+dependency.Name {
			t.Fatal("verified public dependency unexpectedly remained embedded")
		}
		if file.Name != "modrinth.index.json" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var index Index
		err = json.NewDecoder(stream).Decode(&index)
		closeErr := stream.Close()
		if err != nil || closeErr != nil || len(index.Files) != 1 || len(index.Files[0].Downloads) != 1 || index.Files[0].Downloads[0] != dependency.SourceURL || index.Files[0].Hashes["sha512"] != dependency.SHA512 || index.Files[0].Hashes["sha1"] != dependency.SHA1 || index.Files[0].FileSize != dependency.Size {
			t.Fatal("verified dependency metadata differs from index", err, closeErr)
		}
	}
	if err = os.WriteFile(filepath.Join(work, "actual-public-dependency.mrpack"), published.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("official Modrinth hash metadata matched actual Fabric API bytes; public dependency exported with SHA1/SHA512/size, project jar remains embedded")
}
