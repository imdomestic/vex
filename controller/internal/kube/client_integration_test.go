//go:build integration

package kube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestActualKubernetesRoomObjects(t *testing.T) {
	kubeconfig := os.Getenv("CHORUS_IT_KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("CHORUS_IT_KUBECONFIG is required")
	}
	namespace := fmt.Sprintf("chorus-it-%d", time.Now().UnixNano())
	cfg := config.Config{
		Server:    config.Server{InternalBaseURL: "http://10.1.2.137:8790"},
		Minecraft: config.Minecraft{Version: "26.3", Zone: "chorus.internal", Image: "itzg/minecraft-server:java25@sha256:7462fe34a56686719463ff898a674c1a1ce802988138d2bfdeff2507fb1644e8", Whitelist: []string{"linWhite"}},
		Rooms:     config.Rooms{Namespace: namespace, Kubeconfig: kubeconfig, Heap: "4G", MemoryLimit: "5Gi", MaxRunning: map[string]int{"encore": 2, "seraph": 4}},
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	_, err = c.api.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !strings.HasPrefix(namespace, "chorus-it-") {
			t.Fatal("refusing namespace cleanup")
		}
		cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		if e := c.api.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{}); e != nil {
			t.Error(e)
			return
		}
		for {
			_, e := c.api.CoreV1().Namespaces().Get(cleanup, namespace, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return
			}
			if e != nil {
				t.Error(e)
				return
			}
			select {
			case <-cleanup.Done():
				t.Error(cleanup.Err())
				return
			case <-time.After(time.Second):
			}
		}
	})
	version, loader := "26.3", "0.19.5"
	credential := make([]byte, 32)
	if _, err = rand.Read(credential); err != nil {
		t.Fatal(err)
	}
	spec := RoomSpec{
		Room:  model.Room{ID: "main-fabric", Kind: "branch", Loader: "fabric", BuildID: "fabric-fc16eaac252b", Generation: 1},
		Build: model.Build{ID: "fabric-fc16eaac252b", Status: "ready", Minecraft: &version, LoaderVersion: &loader},
		Node:  "encore", RoomToken: hex.EncodeToString(credential), RCONPassword: hex.EncodeToString(credential[0:16]),
	}
	service, secret, workload, err := c.objects(spec)
	if err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	workload.Spec.Replicas = &zero
	if _, err = c.api.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.api.CoreV1().Services(namespace).Create(ctx, service, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.api.AppsV1().StatefulSets(namespace).Create(ctx, workload, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, err := c.ObserveRoom(ctx, spec.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exists || status.PodExists || status.Ready || status.Generation != 1 || status.BuildID != spec.Build.ID {
		t.Fatalf("unexpected stopped room state: %+v", status)
	}
	host := "main-fabric.100-64-0-43.sslip.io"
	if err = c.SyncHost(ctx, spec.Room.ID, host); err != nil {
		t.Fatal(err)
	}
	changed, err := c.api.CoreV1().Services(namespace).Get(ctx, service.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Annotations["mc-router.itzg.me/externalServerName"] != host {
		t.Fatal("zone did not propagate to the real Service")
	}
	if err = c.ScaleRoom(ctx, spec.Room.ID, 0); err != nil {
		t.Fatal(err)
	}
	nodes, err := c.Nodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range nodes {
		if node.Name == "encore" && node.Ready {
			found = true
		}
	}
	if !found {
		t.Fatal("encore is not Ready")
	}
	before, err := c.api.CoreV1().Secrets(namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec.Room.BuildID, spec.Build.ID = "fabric-000000000000", "fabric-000000000000"
	if err = c.EnsureRoom(ctx, spec); err == nil {
		t.Fatal("changing build within one generation was accepted")
	}
	after, err := c.api.CoreV1().Secrets(namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Fatal("conflicting room update changed credentials")
	}
	if err = c.DeleteArchivedRoom(ctx, spec.Room, model.Snapshot{}); err == nil {
		t.Fatal("branch world deletion was accepted")
	}
}
