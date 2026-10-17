//go:build integration

package kubernetes

import (
	"context"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestActualKubernetesRoomObjects(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	spec := integrationRoomSpec(t, c)
	service, secret, workload, err := c.objects(spec)
	if err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	workload.Spec.Replicas = &zero
	if _, err = c.api.CoreV1().Secrets(c.ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.api.CoreV1().Services(c.ns).Create(ctx, service, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.api.AppsV1().StatefulSets(c.ns).Create(ctx, workload, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	ref, err := c.Reference(spec.Room)
	if err != nil {
		t.Fatal(err)
	}
	status, err := c.ObserveRoom(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exists || status.ProcessExists || status.Ready || status.Generation != 1 || status.BuildID != spec.Build.ID {
		t.Fatalf("unexpected stopped room state: %+v", status)
	}
	c.cfg.Server.Zone = "changed.vex.internal"
	host := spec.Room.ID + "--" + c.cfg.ID + "." + c.cfg.Server.Zone
	if err = c.Sync(ctx, ref, host); err != nil {
		t.Fatal(err)
	}
	changed, err := c.api.CoreV1().Services(c.ns).Get(ctx, service.Name, metav1.GetOptions{})
	if err != nil || changed.Annotations["mc-router.itzg.me/externalServerName"] != host {
		t.Fatal("zone did not propagate to the actual Service", err)
	}
	if err = c.StopRoom(ctx, ref, runtime.StopOptions{Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	nodes, err := c.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !nodes[0].Ready || nodes[0].RootFreeBytes == nil || *nodes[0].RootFreeBytes <= 0 {
		t.Fatal("configured node did not provide Ready and real disk statistics")
	}
	before, err := c.api.CoreV1().Secrets(c.ns).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec.Room.BuildID, spec.Build.ID = "fabric-000000000000", "fabric-000000000000"
	if _, err = c.EnsureRoom(ctx, spec); err == nil {
		t.Fatal("changing build within one generation was accepted")
	}
	after, err := c.api.CoreV1().Secrets(c.ns).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil || before.ResourceVersion != after.ResourceVersion {
		t.Fatal("conflicting room update changed credentials", err)
	}
	if err = c.DeleteRoom(ctx, ref, runtime.DeleteOptions{}); err == nil {
		t.Fatal("branch world deletion was accepted")
	}
	if err = c.Remove(ctx, ref); err != nil {
		t.Fatal(err)
	}
	changed, err = c.api.CoreV1().Services(c.ns).Get(ctx, service.Name, metav1.GetOptions{})
	if err != nil || changed.Annotations["mc-router.itzg.me/externalServerName"] != "" {
		t.Fatal("route removal failed", err)
	}
}
