//go:build integration

package kubernetes_test

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/slp"
	"github.com/imdomestic/vex/controller/internal/wake"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRealSleepingRouteRestoresMinecraftAndPreservesWorld(t *testing.T) {
	h := newLifecycleHarness(t, "wake", false)
	apiURL, err := url.Parse(h.baseURL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(apiURL.Hostname(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h.manager.Cfg.Wake = config.Wake{Enabled: true, Listen: listener.Addr().String(), Endpoint: listener.Addr().String(), RateLimit: "1m"}
	h.manager.Cfg.Rooms.DefaultSeed = "empty"
	// 独立镜像从真实现有镜像获取对象，测试刷新不修改生产镜像。
	mirror, err := gitmirror.New(os.Getenv("VEX_IT_MIRROR"), filepath.Join(h.root, "wake-mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.manager.Git = mirror
	if err = h.manager.SyncBranches(h.ctx); err != nil {
		t.Fatal(err)
	}
	registered, err := h.manager.List(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var room model.Room
	for _, candidate := range registered {
		if candidate.Kind == "branch" && candidate.SHA == h.manifest.SHA && candidate.Loader == h.manifest.Loader {
			room = candidate
			break
		}
	}
	if room.ID == "" {
		t.Fatal("actual source mirror has no branch matching the ready fixture")
	}
	lookup := func(ctx context.Context, project, id string) (model.Room, error) {
		if project != h.cfg.ID {
			return model.Room{}, model.Failure("not_found", "project does not exist")
		}
		return h.manager.Get(ctx, id)
	}
	authorize := func(ctx context.Context, project, id, name string, player uuid.UUID) error {
		if project != h.cfg.ID {
			return model.Failure("unauthorized", "project differs")
		}
		return h.manager.WakeAuthorize(ctx, id, name, player)
	}
	start := func(ctx context.Context, project, id, name string, player uuid.UUID) error {
		if project != h.cfg.ID {
			return model.Failure("unauthorized", "project differs")
		}
		return h.manager.WakeStart(ctx, id, name, player)
	}
	responder, err := wake.New(wake.Options{Zone: h.cfg.Server.Zone, RateLimit: time.Minute, SourceAllowed: func(ctx context.Context, source netip.Addr) (bool, error) {
		return h.backend.RouterSource(ctx, source.String())
	}}, lookup, authorize, start)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- responder.Serve(serveCtx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := <-finished; err != nil {
			t.Error(err)
		}
	})
	room = h.current(room.ID)
	h.wait("real sleeping route through mc-router", time.Minute, func() bool {
		status, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, room.Host, room.Port)
		return err == nil && status.Description.ClearString() == wake.SleepingMOTD && !slp.Matches(status, room)
	})
	services, err := h.cluster.CoreV1().Services(h.ns).List(h.ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/room=" + room.ID})
	if err != nil || len(services.Items) != 1 || len(services.Items[0].Spec.Selector) != 0 {
		t.Fatal("sleeping Service must have no selector", err)
	}
	service := services.Items[0]
	slice, err := h.cluster.DiscoveryV1().EndpointSlices(h.ns).Get(h.ctx, service.Name+"-wake", metav1.GetOptions{})
	if err != nil || len(slice.Endpoints) != 1 || len(slice.OwnerReferences) != 1 || slice.OwnerReferences[0].UID != service.UID {
		t.Fatal("sleeping EndpointSlice is not attached to the actual Service", err)
	}
	if _, err = slp.Probe(h.ctx, listener.Addr().String(), room.Host, room.Port); err == nil {
		t.Fatal("direct non-router connection reached the wake responder")
	}
	reason := probeWakeLogin(t, h, room, uuid.New())
	if reason != "此账号没有唤醒该项目房间的权限" {
		t.Fatal("nonmember did not receive the expected rejection")
	}
	unchanged, err := h.manager.Get(h.ctx, room.ID)
	if err != nil || unchanged.Generation != 0 || unchanged.State != "stopped" {
		t.Fatal("nonmember login-start changed the room", err)
	}
	var player uuid.UUID
	for _, value := range h.manager.Auth.MinecraftPlayers(h.cfg.ID, "members") {
		if id, parseErr := uuid.Parse(value); parseErr == nil && id != uuid.Nil {
			player = id
			break
		}
	}
	if player == uuid.Nil {
		t.Fatal("actual project configuration has no bound Minecraft UUID for wake verification")
	}
	if reason = probeWakeLogin(t, h, room, player); reason != "房间正在启动，约 1 分钟后重新连接" {
		t.Fatal("authorized login identity did not request the actual Minecraft start")
	}
	if reason = probeWakeLogin(t, h, room, player); reason != "此房间刚刚收到唤醒请求，请稍候重新连接" {
		t.Fatal("same-room wake rate limit did not reject the repeated request")
	}
	t.Log("actual mc-router sleeping MOTD has no ready identity; nonmember and direct-source requests rejected; configured login-start identity triggered start and the 60-second rate limit")
	h.wait("real Minecraft route restoration", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			t.Fatal("actual Minecraft failed during route restoration")
		}
		return room.State == "ready"
	})
	currentService, err := h.cluster.CoreV1().Services(h.ns).Get(h.ctx, service.Name, metav1.GetOptions{})
	if err != nil || currentService.UID != service.UID || len(currentService.Spec.Selector) == 0 {
		t.Fatal("ready route did not preserve Service identity and restore selector", err)
	}
	pod, err := h.cluster.CoreV1().Pods(h.ns).Get(h.ctx, service.Name+"-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	selectedNode := false
	for _, node := range h.cfg.Runtime.Kubernetes.Nodes {
		selectedNode = selectedNode || pod.Spec.NodeName == node.K8sName
	}
	if room.Node == nil || *room.Node != pod.Spec.NodeName || !selectedNode {
		t.Fatal("restored route did not use the selected actual Kubernetes node")
	}
	t.Logf("restored sleeping route on actual node %s with Pod %s", pod.Spec.NodeName, pod.Name)
	if _, err = h.cluster.DiscoveryV1().EndpointSlices(h.ns).Get(h.ctx, slice.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("ready route retained its manual wake EndpointSlice", err)
	}
	pvcName := "data-" + service.Name + "-0"
	pvc, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, pvcName, metav1.GetOptions{})
	if err != nil || string(pvc.Status.Phase) != "Bound" {
		t.Fatal("real room world is not bound", err)
	}
	sleepAttempt := room
	sleepAttempt.State = "stopped"
	if err = h.backend.Sleep(h.ctx, sleepAttempt, room.Host, listener.Addr().String()); err == nil {
		t.Fatal("sleeping route accepted an active Minecraft process")
	}
	status, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, room.Host, room.Port)
	if err != nil || !slp.Matches(status, room) {
		t.Fatal("active Minecraft route was changed by a rejected sleeping request", err)
	}
	t.Logf("actual Minecraft routed identity ready: project=%s room=%s generation=%d; active process protected", room.Project, room.ID, room.Generation)
	room = h.stop(room.ID)
	if room.State != "stopped" || room.LastSnapshot == nil {
		t.Fatal("branch stop did not preserve its world and snapshot")
	}
	retained, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, pvcName, metav1.GetOptions{})
	if err != nil || retained.UID != pvc.UID || string(retained.Status.Phase) != "Bound" {
		t.Fatal("sleeping branch lost its retained world PVC", err)
	}
	h.wait("sleeping route after normal archive", time.Minute, func() bool {
		status, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, room.Host, room.Port)
		return err == nil && status.Description.ClearString() == wake.SleepingMOTD && !slp.Matches(status, room)
	})
	t.Logf("normal stop archived the real world and restored sleeping route; PVC UID preserved: %s", retained.UID)
	if _, _, err = h.manager.Label(h.ctx, room.ID, "finished"); err != nil {
		t.Fatal(err)
	}
	closed := h.current(room.ID)
	if closed.State != "closed" || h.manager.WakeAuthorize(h.ctx, room.ID, "VexProbe", player) == nil {
		t.Fatal("closed branch remained eligible for wake")
	}
	if _, err = h.cluster.CoreV1().Services(h.ns).Get(h.ctx, service.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("closed branch retained its wake Service", err)
	}
	if _, err = h.cluster.DiscoveryV1().EndpointSlices(h.ns).Get(h.ctx, slice.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("closed branch retained its wake EndpointSlice", err)
	}
	retained, err = h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, pvcName, metav1.GetOptions{})
	if err != nil || retained.UID != pvc.UID || string(retained.Status.Phase) != "Bound" {
		t.Fatal("closing the wake route changed the retained branch world", err)
	}
}

func probeWakeLogin(t *testing.T, h *lifecycleHarness, room model.Room, player uuid.UUID) string {
	t.Helper()
	socket, err := (&net.Dialer{}).DialContext(h.ctx, "tcp", h.cfg.Server.RouterAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err = socket.SetDeadline(time.Now().Add(25 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn := mcnet.WrapConn(socket)
	if err = conn.WritePacket(pk.Marshal(0, pk.VarInt(774), pk.String(room.Host), pk.UnsignedShort(room.Port), pk.VarInt(2))); err != nil {
		t.Fatal(err)
	}
	if err = conn.WritePacket(pk.Marshal(0, pk.String("VexProbe"), pk.UUID(player))); err != nil {
		t.Fatal(err)
	}
	var response pk.Packet
	if err = conn.ReadPacket(&response); err != nil || response.ID != 0 {
		t.Fatal("actual responder did not return a login disconnect", err)
	}
	var data pk.String
	if err = response.Scan(&data); err != nil {
		t.Fatal(err)
	}
	var message struct {
		Text string `json:"text"`
	}
	if err = json.Unmarshal([]byte(data), &message); err != nil {
		t.Fatal(err)
	}
	return message.Text
}
