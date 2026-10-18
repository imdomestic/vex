//go:build integration

package kubernetes_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/slp"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRealEULAAcceptanceStartsMinecraftAndArchives(t *testing.T) {
	h := newLifecycleHarness(t, "eula", false)
	if !h.cfg.Minecraft.AcceptEULA {
		t.Fatal("actual Minecraft fixture requires explicit EULA acceptance")
	}
	room, _, err := h.manager.Create(h.ctx, "commit", h.manifest.SHA, h.manifest.Loader, "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	manager := h.manager
	start := func(expected int, output any) {
		t.Helper()
		route := "/v2/projects/" + h.cfg.ID + "/rooms/" + room.ID + "/start"
		request, err := http.NewRequestWithContext(h.ctx, http.MethodPost, h.baseURL+route, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+h.token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal("actual room start HTTP request failed")
		}
		defer response.Body.Close()
		if response.StatusCode != expected {
			t.Fatalf("actual room start returned HTTP %d; expected %d", response.StatusCode, expected)
		}
		if err = json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal("actual room start response is not valid JSON")
		}
	}

	assertNoEULARoomResources(t, h)
	manager.Cfg.Minecraft.AcceptEULA = false
	var rejected struct {
		Error model.Error `json:"error"`
	}
	start(http.StatusConflict, &rejected)
	if rejected.Error.Code != "eula_not_accepted" {
		t.Fatal("EULA rejection returned a different error code")
	}
	unchanged := h.current(room.ID)
	if unchanged.State != room.State || unchanged.Generation != room.Generation || unchanged.Generation != 0 {
		t.Fatal("rejected EULA request changed the room lifecycle")
	}
	leases, err := store.All[model.LeaseRecord](h.ctx, h.db, "lease")
	if err != nil || len(leases) != 0 {
		t.Fatal("rejected EULA request created a lease", err)
	}
	assertNoEULARoomResources(t, h)
	t.Log("accept_eula=false: actual HTTP 409/eula_not_accepted; generation unchanged, zero leases and room resources")

	manager.Cfg.Minecraft.AcceptEULA = true
	var accepted model.StartResult
	start(http.StatusAccepted, &accepted)
	if h.manager != manager || accepted.Room.ID != room.ID || accepted.Room.Generation != 1 || accepted.Room.BuildID != h.manifest.ID {
		t.Fatal("EULA acceptance did not start the same manager and room with the actual ready build")
	}
	h.wait("EULA-accepted Minecraft readiness", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			t.Fatal("actual EULA-accepted Minecraft process failed")
		}
		return room.State == "ready"
	})
	ref := h.ref(room.ID)
	observed, err := h.backend.ObserveRoom(h.ctx, ref)
	if err != nil || !observed.Ready || !observed.IdentityMatches || observed.Generation != room.Generation || observed.BuildID != h.manifest.ID {
		t.Fatal("actual Minecraft process identity does not match the accepted room", err)
	}
	status, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, room.Host, room.Port)
	if err != nil || !slp.Matches(status, room) {
		t.Fatal("actual routed Minecraft SLP identity does not match", err)
	}
	t.Logf("accept_eula=true: actual Minecraft and routed SLP ready; project=%s room=%s generation=%d build=%s", h.cfg.ID, room.ID, room.Generation, room.BuildID)

	room = h.stop(room.ID)
	if room.State != "closed" || room.ClosedAt == nil {
		t.Fatal("normal stop did not close the commit room")
	}
	observed, err = h.backend.ObserveRoom(h.ctx, ref)
	if err != nil || observed.ProcessExists {
		t.Fatal("Minecraft process remains after normal stop", err)
	}
	var snapshot model.Snapshot
	snapshotID := fmt.Sprintf("%s-g%d", room.ID, room.Generation)
	if err = h.db.Get(h.ctx, "snapshot", snapshotID, &snapshot); err != nil || !snapshot.LocalAvailable || snapshot.Project != h.cfg.ID || snapshot.RoomGeneration != room.Generation || snapshot.BuildID != h.manifest.ID {
		t.Fatal("normal stop did not record the actual world snapshot", err)
	}
	path := filepath.Join(h.cfg.DataDir, filepath.FromSlash(snapshot.Path))
	digest, size, err := worldarchive.Digest(path)
	if err != nil || digest != snapshot.SHA256 || size != snapshot.Size || size == 0 {
		t.Fatal("actual world snapshot differs from its recorded checksum", err)
	}
	if err = worldarchive.Validate(path, *h.manifest.Minecraft, h.manifest.WorldExtraRootEntries...); err != nil {
		t.Fatal("actual stopped Minecraft world archive is invalid", err)
	}
	if err = manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.All[model.Transfer](h.ctx, h.db, "transfer")
	if err != nil || len(tasks) != 2 {
		t.Fatal("normal stop requires world and metadata archive transfers", err)
	}
	parts := map[string]bool{}
	for _, task := range tasks {
		if task.ObjectID != snapshot.ID || task.Kind != "snapshot" || task.Status != "verified" || task.VerifiedAt == nil || task.Part != "world" && task.Part != "metadata" || parts[task.Part] {
			t.Fatal("actual local archive transfer is not independently verified")
		}
		parts[task.Part] = true
		archived := filepath.Join(h.cfg.Archive.Local.Directory, filepath.FromSlash(task.RemotePath))
		digest, size, err := worldarchive.Digest(archived)
		if err != nil || digest != task.SHA256 || size != task.Size {
			t.Fatal("archived file differs from its transfer checksum", err)
		}
	}
	t.Logf("normal Minecraft stop and local archive verified: snapshot=%s sha256=%s size=%d; isolated namespace cleanup uses its captured UID", snapshot.ID, snapshot.SHA256, snapshot.Size)
}

func assertNoEULARoomResources(t *testing.T, h *lifecycleHarness) {
	t.Helper()
	pods, err := h.cluster.CoreV1().Pods(h.ns).List(h.ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) != 0 {
		t.Fatal("unexpected Pod in EULA-rejected project", err)
	}
	jobs, err := h.cluster.BatchV1().Jobs(h.ns).List(h.ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatal("unexpected Job in EULA-rejected project", err)
	}
	workloads, err := h.cluster.AppsV1().StatefulSets(h.ns).List(h.ctx, metav1.ListOptions{})
	if err != nil || len(workloads.Items) != 0 {
		t.Fatal("unexpected StatefulSet in EULA-rejected project", err)
	}
	claims, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).List(h.ctx, metav1.ListOptions{})
	if err != nil || len(claims.Items) != 0 {
		t.Fatal("unexpected PVC in EULA-rejected project", err)
	}
	services, err := h.cluster.CoreV1().Services(h.ns).List(h.ctx, metav1.ListOptions{})
	if err != nil || len(services.Items) != 0 {
		t.Fatal("unexpected Service in EULA-rejected project", err)
	}
	secrets, err := h.cluster.CoreV1().Secrets(h.ns).List(h.ctx, metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 0 {
		t.Fatal("unexpected room Secret in EULA-rejected project", err)
	}
}
