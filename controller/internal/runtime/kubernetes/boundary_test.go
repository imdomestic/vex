package kubernetes

import (
	"context"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestReferenceProjectAndHostnameBoundary(t *testing.T) {
	c := &Client{cfg: config.ProjectConfig{ID: "project-one", Runtime: config.Runtime{Kubernetes: config.Kubernetes{NamespacePrefix: "vex-p-", SystemNamespace: "vex-system"}}}, ns: "vex-p-project-one"}
	room := model.Room{Project: c.cfg.ID, ID: "main-fabric", Kind: "branch", Generation: 1, BuildID: "fabric-0123456789ab"}
	ref, err := c.Reference(room)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := c.referenceRoom(ref)
	if err != nil || decoded.Project != room.Project || decoded.ID != room.ID || decoded.Generation != room.Generation {
		t.Fatal("reference did not preserve runtime identity", err)
	}
	for _, field := range []string{"project", "namespace", "objectName", "generation", "build", "kind"} {
		original := ref.Attributes[field]
		ref.Attributes[field] = ""
		if _, err = c.referenceRoom(ref); err == nil {
			t.Fatalf("accepted missing resource field %s", field)
		}
		ref.Attributes[field] = original
	}
	ref.WorldID = "vex-p-other/data-room-main-fabric-0"
	if _, err = c.referenceRoom(ref); err == nil {
		t.Fatal("accepted foreign world identity")
	}
	room.ID = strings.Repeat("r", 63-len(c.cfg.ID)-2)
	if _, err = c.Reference(room); err != nil {
		t.Fatal("valid full hostname label was rejected", err)
	}
	room.ID += "r"
	if _, err = c.Reference(room); err == nil {
		t.Fatal("accepted a hostname label exceeding 63 bytes")
	}
	room.ID, room.Project = "main-fabric", "other"
	if _, err = c.Reference(room); err == nil {
		t.Fatal("accepted a foreign project")
	}
}

func TestGeneratedObjectNamesAndNodeRoles(t *testing.T) {
	for _, id := range []string{"main-fabric", strings.Repeat("a", 63)} {
		name, err := objectName(id)
		if err != nil || len(validation.IsDNS1123Label(name)) != 0 || len(validation.IsDNS1123Label(name+"-0")) != 0 {
			t.Fatal("generated workload name is invalid", err)
		}
		if len(validation.IsDNS1123Label(archiveJobName(id, 9223372036854775807))) != 0 {
			t.Fatal("generated archive name is invalid")
		}
	}
	c := &Client{cfg: config.ProjectConfig{Runtime: config.Runtime{Kubernetes: config.Kubernetes{Nodes: map[string]config.Node{
		"persistent": {K8sName: "node-a", Roles: []string{"persistent"}, MaxRooms: 2},
		"ephemeral":  {K8sName: "node-b", Roles: []string{"ephemeral"}, MaxRooms: 3},
	}}}}}
	if !c.nodeAllows("node-a", "branch") || c.nodeAllows("node-b", "branch") || !c.nodeAllows("node-b", "world") || c.nodeAllows("unknown", "commit") {
		t.Fatal("configured role boundaries were not enforced")
	}
}

func TestInstanceNamespaceBoundaries(t *testing.T) {
	for _, prefix := range []string{"", "vex-p", "../", "Vex-", "-", strings.Repeat("a", 63) + "-"} {
		if _, err := projectNamespace(config.Kubernetes{NamespacePrefix: prefix, SystemNamespace: "vex-system"}, "project"); err == nil {
			t.Fatalf("accepted invalid namespace prefix %q", prefix)
		}
	}
	for _, system := range []string{"", "VEX", "../system", "vex-p-project"} {
		if _, err := projectNamespace(config.Kubernetes{NamespacePrefix: "vex-p-", SystemNamespace: system}, "project"); err == nil {
			t.Fatalf("accepted invalid system namespace %q", system)
		}
	}
	cfg := config.Kubernetes{NamespacePrefix: "instance-", SystemNamespace: "instance-system"}
	if namespace, err := projectNamespace(cfg, "project"); err != nil || namespace != "instance-project" {
		t.Fatal("valid instance namespace was rejected", namespace, err)
	}
	c := &Client{cfg: config.ProjectConfig{ID: "project", Runtime: config.Runtime{Kubernetes: cfg}}, ns: "vex-p-project"}
	if _, err := c.Reference(model.Room{Project: "project", ID: "main-fabric", Generation: 1, BuildID: "fabric-0123456789ab"}); err == nil {
		t.Fatal("accepted a client namespace outside the configured prefix")
	}
}

func TestProjectPolicyPortAndMemoryBoundaries(t *testing.T) {
	c := &Client{cfg: config.ProjectConfig{ID: "project", Server: config.Server{InternalBaseURL: "http://10.1.2.137:8790"}, Rooms: config.EffectiveRooms{MemoryLimit: "5Gi"}, Quota: config.Quota{MaxRooms: 2}, Runtime: config.Runtime{Kubernetes: config.Kubernetes{NamespacePrefix: "vex-p-", SystemNamespace: "instance-system"}}}, ns: "vex-p-project"}
	quota, limits, policy, err := c.projectObjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pods := quota.Spec.Hard[corev1.ResourcePods]
	if pods.Value() != 2 {
		t.Fatal("project room quota differs")
	}
	memory := quota.Spec.Hard[corev1.ResourceLimitsMemory]
	if memory.Value() != 10<<30 || len(limits.Spec.Limits) != 1 {
		t.Fatal("project memory quota differs")
	}
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].Ports) != 1 || policy.Spec.Ingress[0].Ports[0].Port.IntVal != 25565 {
		t.Fatal("room ingress permits unexpected ports")
	}
	if policy.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "instance-system" {
		t.Fatal("router ingress does not use the configured system namespace")
	}
	router := policy.Spec.Ingress[0]
	if len(router.From) != 1 || router.From[0].IPBlock != nil || router.From[0].PodSelector == nil ||
		router.From[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "mc-router" ||
		router.From[0].PodSelector.MatchLabels["app.kubernetes.io/part-of"] != "vex" {
		t.Fatal("room ingress is not restricted to managed router Pods")
	}
	api := policy.Spec.Egress[1]
	if len(api.To) != 1 || api.To[0].IPBlock.CIDR != "10.1.2.137/32" || len(api.Ports) != 1 || api.Ports[0].Port.IntVal != 8790 {
		t.Fatal("API egress exception is not restricted to its endpoint")
	}
	for _, cidr := range []string{"10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"} {
		found := false
		for _, blocked := range policy.Spec.Egress[2].To[0].IPBlock.Except {
			found = found || blocked == cidr
		}
		if !found {
			t.Fatalf("public egress omitted private range %s", cidr)
		}
	}
}
