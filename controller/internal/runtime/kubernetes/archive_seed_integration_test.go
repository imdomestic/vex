//go:build integration

package kubernetes_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/slp"
	"github.com/imdomestic/vex/controller/internal/testfixture"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	corev1 "k8s.io/api/core/v1"
)

func seedArchiveDigests(t *testing.T, archive string) map[string]string {
	t.Helper()
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	digests := map[string]string{}
	for {
		member, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(member.Name, "./")
		if name != "world/level.dat" && !(strings.Contains(name, "/region/") && strings.HasSuffix(name, ".mca")) {
			continue
		}
		if member.Typeflag != tar.TypeReg {
			t.Fatal("seed level or region member is not a regular file")
		}
		digest := sha256.New()
		if _, err = io.Copy(digest, reader); err != nil {
			t.Fatal(err)
		}
		digests[name] = fmt.Sprintf("%x", digest.Sum(nil))
	}
	if digests["world/level.dat"] == "" || len(digests) < 2 {
		t.Fatal("actual Minecraft seed requires level.dat and generated region data")
	}
	return digests
}

func verifySeedInitDigests(t *testing.T, h *lifecycleHarness, roomID string, expected map[string]string) {
	t.Helper()
	ref := h.ref(roomID)
	data, err := h.cluster.CoreV1().Pods(h.ns).GetLogs(ref.Attributes["objectName"]+"-0", &corev1.PodLogOptions{Container: "seed-world"}).DoRaw(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	verified := map[string]bool{}
	for {
		var entry struct {
			Event  string `json:"event"`
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}
		if err = decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal("seed init digest log is not valid JSON", err)
		}
		if entry.Event != "seed-file-sha256" || expected[entry.Path] != entry.SHA256 || verified[entry.Path] {
			t.Fatal("seed init bytes differ from the restored immutable archive")
		}
		verified[entry.Path] = true
	}
	if len(verified) != len(expected) {
		t.Fatal("seed init did not report every level.dat and region digest")
	}
	t.Logf("verified %d extracted level.dat and region SHA256 values before Minecraft startup", len(verified))
}

func realPackCLI(t *testing.T, h *lifecycleHarness) {
	t.Helper()
	binary, versionFile := os.Getenv("VEX_IT_CLI"), testfixture.File(t, "minecraft-26.3-version.json")
	if binary == "" {
		t.Fatal("pack CLI verification requires the compiled Vex CLI")
	}
	data, err := os.ReadFile(versionFile)
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		ID   string `json:"id"`
		Pack struct {
			Major int `json:"resource_major"`
			Minor int `json:"resource_minor"`
		} `json:"pack_version"`
	}
	if err = json.Unmarshal(data, &version); err != nil {
		t.Fatal(err)
	}
	if version.ID != *h.manifest.Minecraft || version.Pack.Major <= 0 {
		t.Fatal("official resource-pack format differs from the actual build")
	}
	metadata, err := json.Marshal(map[string]any{"pack": map[string]any{"description": "Vex API integration", "min_format": []int{version.Pack.Major, version.Pack.Minor}, "max_format": []int{version.Pack.Major, version.Pack.Minor}}})
	if err != nil {
		t.Fatal(err)
	}
	translation, err := json.Marshal(map[string]string{"vex.integration": "Vex integration"})
	if err != nil {
		t.Fatal(err)
	}
	packPath := filepath.Join(h.root, "resource-pack.zip")
	file, err := os.Create(packPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range []struct {
		Name string
		Data []byte
	}{{"pack.mcmeta", metadata}, {"assets/vex/lang/en_us.json", translation}} {
		part, err := writer.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}
	digest, size, err := worldarchive.Digest(packPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf("%x", sha1.Sum(content))
	tokenFile := filepath.Join(h.root, "client.token")
	if err = os.WriteFile(tokenFile, []byte(h.token), 0600); err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(h.root, "client.toml")
	if err = os.WriteFile(configuration, nil, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(arguments ...string) []byte {
		t.Helper()
		args := append([]string{"--config", configuration, "--url", h.baseURL, "--token-file", tokenFile, "--project", h.cfg.ID}, arguments...)
		command := exec.CommandContext(h.ctx, binary, args...)
		for _, variable := range os.Environ() {
			if !strings.HasPrefix(variable, "VEX_") && !strings.HasPrefix(variable, "TMPDIR=") {
				command.Env = append(command.Env, variable)
			}
		}
		command.Env = append(command.Env, "TMPDIR="+filepath.Join(h.cfg.DataDir, ".work"))
		var output, diagnostic bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostic
		if err := command.Run(); err != nil {
			t.Fatalf("actual pack CLI failed: %v: %s", err, diagnostic.String())
		}
		return output.Bytes()
	}
	var first, repeated model.PackRevision
	if err = json.Unmarshal(run("pack", "publish", packPath, "--pack", "integration"), &first); err != nil {
		t.Fatal(err)
	}
	if first.Project != h.cfg.ID || first.ID != "integration" || first.Rev != digest || first.SHA256 != digest || first.SHA1 != legacy || first.Size != size {
		t.Fatal("CLI publication metadata differs from actual ZIP bytes")
	}
	if err = json.Unmarshal(run("pack", "publish", packPath, "--pack", "integration"), &repeated); err != nil {
		t.Fatal(err)
	}
	if repeated != first {
		t.Fatal("identical CLI upload changed the published revision")
	}
	if !bytes.Equal(run("request", "GET", first.URL), content) {
		t.Fatal("CLI download differs from uploaded ZIP")
	}
	t.Logf("actual pack CLI upload and download verified: sha256=%s sha1=%s size=%d", digest, legacy, size)
}

func TestRealLocalSeedStartsMinecraftThroughRouter(t *testing.T) {
	realArchivedSeed(t, false)
}

func TestRealTankSeedStartsMinecraftThroughRouter(t *testing.T) {
	realArchivedSeed(t, true)
}

func realArchivedSeed(t *testing.T, tank bool) {
	t.Helper()
	purpose := "seed"
	if tank {
		purpose = "seedssh"
	}
	h := newLifecycleHarness(t, purpose, tank)
	room := h.startCommit()
	h.rcon(room.ID, "forceload add 0 0")
	h.wait("loaded Minecraft chunk", 30*time.Second, func() bool {
		h.rcon(room.ID, "execute if loaded 0 100 0 run say vex-seed-chunk-loaded")
		return strings.Contains(h.logs(room.ID), "vex-seed-chunk-loaded")
	})
	h.rcon(room.ID, "setblock 0 100 0 minecraft:diamond_block")
	h.rcon(room.ID, "forceload remove 0 0")
	room = h.stop(room.ID)
	if err := h.manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	snapshotID := fmt.Sprintf("%s-g%d", room.ID, room.Generation)
	var snapshot model.Snapshot
	if err := h.db.Get(h.ctx, "snapshot", snapshotID, &snapshot); err != nil || snapshot.TankVerifiedAt == nil || snapshot.LocalAvailable {
		t.Fatal("actual archived snapshot was not verified and evicted", err)
	}
	request := func(method, route string, payload any, output any, expected int) {
		t.Helper()
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(h.ctx, method, h.baseURL+route, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("actual project API request failed")
		}
		defer response.Body.Close()
		if response.StatusCode != expected {
			t.Fatalf("actual project API %s returned %d, expected %d", route, response.StatusCode, expected)
		}
		if output != nil {
			if err = json.NewDecoder(response.Body).Decode(output); err != nil {
				t.Fatal(err)
			}
		}
	}
	var started model.StartResult
	route := "/v2/projects/" + h.cfg.ID + "/rooms/" + room.ID + "/start"
	request(http.MethodPost, route, map[string]string{"seed": "snapshot:" + snapshot.ID}, &started, http.StatusAccepted)
	if started.Room.Generation != room.Generation+1 || started.Room.Phase != "restoring seed" || started.Room.SeedResolved == nil || started.Room.SeedResolved.ID != snapshot.ID {
		t.Fatal("archived seed did not enter the next restoration cycle")
	}
	if err := h.manager.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Get(h.ctx, "snapshot", snapshot.ID, &snapshot); err != nil || !snapshot.LocalAvailable || snapshot.Restore == nil || snapshot.Restore.Status != "succeeded" {
		t.Fatal("automatic snapshot restoration failed", err)
	}
	seedDigests := seedArchiveDigests(t, filepath.Join(h.cfg.DataDir, filepath.FromSlash(snapshot.Path)))
	h.wait("Minecraft restored world ready", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			t.Fatal("actual Minecraft seed startup failed")
		}
		return room.State == "ready"
	})
	verifySeedInitDigests(t, h, room.ID, seedDigests)
	status, err := h.backend.ObserveRoom(h.ctx, h.ref(room.ID))
	if err != nil || !status.Ready || !status.IdentityMatches || status.Generation != room.Generation || status.BuildID != room.BuildID {
		t.Fatal("restored process identity differs from the accepted cycle", err)
	}
	response, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, room.Host, room.Port)
	if err != nil || !slp.Matches(response, room) {
		t.Fatal("actual routed project/room/build/generation identity mismatch", err)
	}
	h.rcon(room.ID, "forceload add 0 0")
	h.wait("restored diamond block", 30*time.Second, func() bool {
		h.rcon(room.ID, "execute if block 0 100 0 minecraft:diamond_block run say vex-seed-world-ok")
		return strings.Contains(h.logs(room.ID), "vex-seed-world-ok")
	})
	h.rcon(room.ID, "forceload remove 0 0")
	realPackCLI(t, h)
	if config.Duration(h.cfg.Rooms.CommitIdle) != 10*time.Minute {
		t.Fatal("natural idle acceptance requires the full ten-minute policy")
	}
	h.wait("actual join lease expires and continuous idle begins", config.Duration(h.cfg.Rooms.JoinLease)+time.Minute, func() bool {
		room = h.current(room.ID)
		return room.State == "ready" && room.IdleSince != nil
	})
	idleSince := *room.IdleSince
	t.Logf("actual empty commit room idle began at %s; waiting for the full 600 seconds", idleSince.Format(time.RFC3339Nano))
	lastProgress := time.Now()
	idleObserved := false
	h.wait("natural ten-minute idle stop and world archive", 13*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State != "ready" && room.State != "stopping" && room.State != "archiving" && room.State != "closed" {
			t.Fatalf("actual idle room left continuous readiness: state=%s phase=%s", room.State, room.Phase)
		}
		elapsed := time.Since(idleSince)
		if room.State == "stopping" || room.State == "archiving" || room.State == "closed" {
			if elapsed < 10*time.Minute {
				t.Fatal("actual room began stopping before the full idle timeout")
			}
			if room.Phase == "idle timeout" {
				idleObserved = true
			}
		}
		if room.State == "ready" && (room.IdleSince == nil || !room.IdleSince.Equal(idleSince)) {
			t.Fatal("continuous idle timestamp changed without a player or lease")
		}
		if time.Since(lastProgress) >= time.Minute {
			t.Logf("actual idle observation: elapsed=%.1fs state=%s", elapsed.Seconds(), room.State)
			lastProgress = time.Now()
		}
		return room.State == "closed"
	})
	if !idleObserved {
		t.Fatal("natural idle timeout transition was not observed")
	}
	if err = h.manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	var finalSnapshot model.Snapshot
	if err = h.db.Get(h.ctx, "snapshot", fmt.Sprintf("%s-g%d", room.ID, room.Generation), &finalSnapshot); err != nil || finalSnapshot.TankVerifiedAt == nil || finalSnapshot.CapturedAt.Before(idleSince.Add(10*time.Minute)) {
		t.Fatal("natural idle did not complete an independently verified new snapshot", err)
	}
	t.Logf("actual ten-minute idle closure and archive verified: generation=%d idleSince=%s capturedAt=%s", room.Generation, idleSince.Format(time.RFC3339Nano), finalSnapshot.CapturedAt.Format(time.RFC3339Nano))
	t.Logf("actual archive restoration passed Minecraft readiness, routed SLP and persisted block verification: project=%s backend=%s", h.cfg.ID, h.cfg.Archive.Backend)
}
