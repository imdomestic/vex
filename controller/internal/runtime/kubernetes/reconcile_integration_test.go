//go:build integration

package kubernetes_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	backend "github.com/imdomestic/vex/controller/internal/runtime/kubernetes"
	"github.com/imdomestic/vex/controller/internal/slp"
	"github.com/imdomestic/vex/controller/internal/worlds"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRealRoomFailureIsolationRouteRecoveryAndCapacity(t *testing.T) {
	h := newLifecycleHarness(t, "failure", false)
	bad := model.Room{Project: h.cfg.ID, ID: "a-error", Kind: "branch", BuildID: h.manifest.ID, Generation: 1, State: "stopped", Label: "testing", Host: "a-error--" + h.cfg.ID + "." + h.cfg.Server.Zone}
	ref, err := h.backend.Reference(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.db.Put(h.ctx, "room", bad.ID, bad); err != nil {
		t.Fatal(err)
	}
	if err = h.db.Put(h.ctx, "room_runtime", bad.ID, rooms.Runtime{Resource: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.cluster.CoreV1().Services(h.ns).Create(h.ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: ref.Attributes["objectName"]}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 25565}}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	room, _, err := h.manager.Create(h.ctx, "commit", h.manifest.SHA, h.manifest.Loader, "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.manager.Start(h.ctx, room.ID, nil, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	h.wait("other room progresses despite real ownership conflict", 6*time.Minute, func() bool {
		err := h.manager.Reconcile(h.ctx)
		if err == nil || !strings.Contains(err.Error(), bad.ID) {
			t.Fatal("actual Service ownership conflict was not reported")
		}
		room, err = h.manager.Get(h.ctx, room.ID)
		if err != nil || room.State == "failed" {
			t.Fatal("healthy room did not continue startup", err)
		}
		return room.State == "ready"
	})
	fault, err := h.manager.Get(h.ctx, bad.ID)
	if err != nil || fault.LastError == nil || fault.LastError.Code != "internal_error" {
		t.Fatal("actual ownership conflict was not persisted", err)
	}
	for _, kind := range []string{"room", "room_runtime"} {
		if err = h.db.Delete(h.ctx, kind, bad.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = h.cluster.CoreV1().Services(h.ns).Delete(h.ctx, ref.Attributes["objectName"], metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:1", time.Second); err == nil {
		conn.Close()
		t.Fatal("route fault port is unexpectedly listening")
	}
	h.manager.Cfg.Server.RouterAddress, h.manager.Cfg.Rooms.StartTimeout = "127.0.0.1:1", "6s"
	h.manager.RequestReconcile(room.Host)
	until := time.Now().Add(8 * time.Second)
	h.wait("route failure beyond startup timeout", 20*time.Second, func() bool {
		room = h.current(room.ID)
		status, err := h.backend.ObserveRoom(h.ctx, h.ref(room.ID))
		if err != nil || !status.ProcessReady || room.State != "starting" || room.Phase != "route unavailable" {
			t.Fatal("routing failure altered the Ready process", err)
		}
		return time.Now().After(until)
	})
	h.manager.Cfg.Server.RouterAddress = h.cfg.Server.RouterAddress
	h.wait("actual router recovers", time.Minute, func() bool { room = h.current(room.ID); return room.State == "ready" })
	response, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, room.Host, room.Port)
	if err != nil || !slp.Matches(response, room) {
		t.Fatal("actual routed SLP identity mismatch", err)
	}
	ref = h.ref(room.ID)
	name := ref.Attributes["objectName"]
	claimName := "data-" + name + "-0"
	claim, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, claimName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	workload, err := h.cluster.AppsV1().StatefulSets(h.ns).Get(h.ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range workload.Spec.Template.Spec.Containers[0].Env {
		variable := &workload.Spec.Template.Spec.Containers[0].Env[i]
		if variable.Name == "JAVA_TOOL_OPTIONS" {
			variable.Value += " -XX:VexInvalidOption=1"
		}
	}
	if _, err = h.cluster.AppsV1().StatefulSets(h.ns).Update(h.ctx, workload, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.wait("actual JVM crash", 3*time.Minute, func() bool {
		pod, err := h.cluster.CoreV1().Pods(h.ns).Get(h.ctx, name+"-0", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "mc" && (status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 || status.LastTerminationState.Terminated != nil && status.LastTerminationState.Terminated.ExitCode != 0) {
				return true
			}
		}
		return false
	})
	room = h.current(room.ID)
	var rt rooms.Runtime
	if err = h.db.Get(h.ctx, "room_runtime", room.ID, &rt); err != nil || rt.ProcessNotReadySince == nil {
		t.Fatal("actual crash did not start continuous failure timing", err)
	}
	h.wait("failed process exits", 3*time.Minute, func() bool { room = h.current(room.ID); return room.State == "failed" })
	status, err := h.backend.ObserveRoom(h.ctx, ref)
	if err != nil || status.ProcessExists {
		t.Fatal("failed room process remains", err)
	}
	retained, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, claimName, metav1.GetOptions{})
	if err != nil || retained.UID != claim.UID {
		t.Fatal("failed room lost its world", err)
	}
	logs, err := h.manager.Log(h.ctx, room.ID)
	if err != nil || !strings.Contains(logs, "VexInvalidOption") {
		t.Fatal("actual JVM diagnostic was not retained", err)
	}
	req, err := http.NewRequestWithContext(h.ctx, "GET", h.baseURL+"/v2/projects/"+h.cfg.ID+"/rooms/"+room.ID+"/log", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	logResponse, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	logData, err := io.ReadAll(logResponse.Body)
	logResponse.Body.Close()
	if err != nil || logResponse.StatusCode != 200 || !strings.Contains(string(logData), "VexInvalidOption") {
		t.Fatal("actual authenticated log endpoint lost the JVM diagnostic", err)
	}
	service, err := h.cluster.CoreV1().Services(h.ns).Get(h.ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	previousUID, previousVersion := service.UID, service.ResourceVersion
	h.manager.Cfg.Server.Zone = "changed.integration.vex.internal"
	client, err := backend.New(h.manager.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.backend, h.manager.Backend, h.manager.Registrar = client, client, client
	before := room
	room = h.current(room.ID)
	if room.State != "failed" || room.Generation != before.Generation || room.BuildID != before.BuildID || room.LastError == nil || room.LastError.Code != "start_timeout" {
		t.Fatal("zone synchronization altered failure identity")
	}
	service, err = h.cluster.CoreV1().Services(h.ns).Get(h.ctx, name, metav1.GetOptions{})
	if err != nil || service.UID != previousUID || service.ResourceVersion == previousVersion || service.Annotations["mc-router.itzg.me/externalServerName"] != room.Host {
		t.Fatal("failed room route was not synchronized", err)
	}
	stable := service.ResourceVersion
	room = h.current(room.ID)
	service, err = h.cluster.CoreV1().Services(h.ns).Get(h.ctx, name, metav1.GetOptions{})
	if err != nil || service.ResourceVersion != stable {
		t.Fatal("unchanged failed route was rewritten", err)
	}
	workload, err = h.cluster.AppsV1().StatefulSets(h.ns).Get(h.ctx, name, metav1.GetOptions{})
	if err != nil || workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 {
		t.Fatal("failed process replicas changed", err)
	}
	metadataBytes, err := os.ReadFile(os.Getenv("VEX_IT_WORLD_METADATA"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata model.WorldMetadata
	if err = json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	document, err := h.manager.Builds.Manifest(h.ctx, h.manifest.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.manager.Worlds.Publish(h.ctx, metadata, os.Getenv("VEX_IT_WORLD_ARCHIVE"), worlds.ManifestPolicy{SHA: h.manifest.SHA, Document: document}); err != nil {
		t.Fatal(err)
	}
	next, _, err := h.manager.Create(h.ctx, "world", h.manifest.SHA, h.manifest.Loader, "", metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.manager.Cfg.Rooms.StartTimeout = "300s"
	if _, _, err = h.manager.Start(h.ctx, next.ID, nil, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	h.wait("released capacity starts the next actual Minecraft", 6*time.Minute, func() bool { next = h.current(next.ID); return next.State == "ready" })
	h.stop(next.ID)
	t.Logf("actual ownership conflict, routed SLP recovery, JVM failure, preserved PVC, failed route synchronization and capacity reuse passed: %s", h.ns)
}
