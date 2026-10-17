//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/filehash"
	"github.com/imdomestic/vex/controller/internal/model"
)

func TestLiveWorldDirectoryPublication(t *testing.T) {
	endpoint, tokenFile := os.Getenv("VEX_IT_URL"), os.Getenv("VEX_IT_TOKEN_FILE")
	project, id := os.Getenv("VEX_IT_PROJECT"), os.Getenv("VEX_IT_WORLD_ID")
	source := os.Getenv("VEX_IT_WORLD_SOURCE")
	if endpoint == "" || tokenFile == "" || project == "" || id == "" || source == "" {
		t.Skip("set VEX_IT_URL, VEX_IT_TOKEN_FILE, VEX_IT_PROJECT, VEX_IT_WORLD_ID and VEX_IT_WORLD_SOURCE for a real Vex server and world directory")
	}
	cleanClientEnvironment(t)
	if !config.ValidID(project) || !config.ValidID(id) {
		t.Fatal("integration project and world IDs are invalid")
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		t.Fatal("integration source must be an actual world directory")
	}
	root := t.TempDir()
	configuration := clientConfigFile(t, "")
	global := []string{"--config", configuration, "--url", endpoint, "--token-file", tokenFile, "-p", project}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	invoke := func(arguments ...string) []byte {
		t.Helper()
		var output bytes.Buffer
		if err := run(ctx, append(append([]string{}, global...), arguments...), &output); err != nil {
			t.Fatal(err)
		}
		return output.Bytes()
	}
	var first model.WorldRevision
	if err = json.Unmarshal(invoke("world", "publish", source, "--world", id, "--work-dir", filepath.Join(root, "directory")), &first); err != nil {
		t.Fatal(err)
	}
	archive, err := directoryArchive(source, filepath.Join(root, "archive"))
	if err != nil {
		t.Fatal(err)
	}
	var repeated model.WorldRevision
	if err = json.Unmarshal(invoke("world", "publish", archive, "--world", id), &repeated); err != nil {
		t.Fatal(err)
	}
	if first.Project != project || first.ID != id || repeated.Rev != first.Rev || !repeated.CreatedAt.Equal(first.CreatedAt) || repeated.World != first.World {
		t.Fatal("actual directory and tar publication did not preserve the immutable revision")
	}
	expected, err := filehash.File(archive)
	if err != nil {
		t.Fatal(err)
	}
	if first.World.SHA1 != expected.SHA1 || first.World.SHA256 != expected.SHA256 || first.World.SHA512 != expected.SHA512 || first.World.Size != expected.Size {
		t.Fatal("published world hashes differ from actual local archive")
	}
	var shown model.World
	if err = json.Unmarshal(invoke("world", "show", id), &shown); err != nil || shown.Project != project || shown.LatestRev != first.Rev {
		t.Fatal("world show did not observe the published revision")
	}
	var listed []model.World
	if err = json.Unmarshal(invoke("world", "list"), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, world := range listed {
		if world.Project != project {
			t.Fatal("world list returned another project's record")
		}
		found = found || world.ID == id && world.LatestRev == first.Rev
	}
	if !found {
		t.Fatal("world list omitted the published revision")
	}
	if !strings.HasPrefix(first.World.URL, "/v2/projects/"+project+"/") {
		t.Fatal("published download URL lacks project identity")
	}
	c, err := newClient(config.Client{URL: endpoint, TokenFile: tokenFile, Project: project, Timeout: "2m"}, false)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.do(ctx, requestPlan{Method: http.MethodGet, Path: first.World.URL})
	if err != nil {
		t.Fatal(err)
	}
	downloaded, digestErr := filehash.Reader(response.Body)
	closeErr := response.Body.Close()
	if digestErr != nil || closeErr != nil || downloaded != expected {
		t.Fatal("actual world download differs from the uploaded archive")
	}
	t.Logf("project=%s world=%s revision=%s size=%d; directory/tar idempotency and three download hashes passed", project, id, first.Rev, expected.Size)
}
