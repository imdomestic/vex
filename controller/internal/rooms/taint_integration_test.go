//go:build integration

package rooms

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	backend "github.com/imdomestic/vex/controller/internal/runtime/kubernetes"
	"github.com/imdomestic/vex/controller/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

func TestRealSeraphTaintExcludesRoomScheduling(t *testing.T) {
	kubeconfig, work := os.Getenv("VEX_IT_KUBECONFIG"), os.Getenv("VEX_IT_WORK")
	if kubeconfig == "" || work == "" || os.Getenv("VEX_IT_TAINT_NODE") != "seraph" {
		t.Skip("requires actual k3s, ignored workspace and explicit seraph taint authorization")
	}
	if !filepath.IsAbs(work) || !strings.Contains(filepath.Clean(work), "/.work/") {
		t.Fatal("test evidence must stay in the ignored workspace")
	}
	evidence, err := os.MkdirTemp(work, "seraph-taint-")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := kubernetes.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	original, err := cluster.CoreV1().Nodes().Get(ctx, "seraph", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeEvidence := func(name string, value any) {
		t.Helper()
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(evidence, name+".json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEvidence("before", map[string]any{"node": original.Name, "uid": original.UID, "taints": original.Spec.Taints})
	cfg := config.ProjectConfig{
		ID: "vex-it-taint", DataDir: evidence,
		Quota: config.Quota{MaxRooms: 1},
		Runtime: config.Runtime{Backend: "kubernetes", Kubernetes: config.Kubernetes{
			KubeconfigFile: kubeconfig, NamespacePrefix: "vex-p-", SystemNamespace: "vex-system",
			Nodes: map[string]config.Node{"seraph": {K8sName: "seraph", Roles: []string{"ephemeral"}, MaxRooms: 4, Priority: 10}},
		}},
	}
	client, err := backend.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(evidence, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scoped, err := db.WithProject(cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Cfg: cfg, DB: scoped, Backend: client}
	room := model.Room{Project: cfg.ID, ID: "scheduling-check", Kind: "commit", State: "queued"}
	verify := func(stage string, schedulable bool) {
		t.Helper()
		nodes, err := client.Capacity(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 1 || nodes[0].Name != "seraph" || !nodes[0].Ready || nodes[0].Schedulable != schedulable {
			t.Fatalf("%s: actual node availability did not match expectation", stage)
		}
		selected, err := manager.chooseNode(ctx, room, nodes)
		if err != nil {
			t.Fatal(err)
		}
		if schedulable && selected != "seraph" || !schedulable && selected != "" {
			t.Fatalf("%s: actual capacity and room selection disagree: %q", stage, selected)
		}
		writeEvidence(stage, map[string]any{"nodes": nodes, "selected": selected})
		t.Logf("%s: seraph Ready=true Schedulable=%t selected=%q", stage, schedulable, selected)
	}
	verify("baseline", true)
	taint := corev1.Taint{Key: "integration.vex.imdomestic.com/taint-" + uuid.NewString(), Value: "test", Effect: corev1.TaintEffectNoSchedule}
	remove := func(cleanup context.Context) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			node, err := cluster.CoreV1().Nodes().Get(cleanup, "seraph", metav1.GetOptions{})
			if err != nil {
				return err
			}
			if node.UID != original.UID {
				return fmt.Errorf("seraph node identity changed")
			}
			kept := make([]corev1.Taint, 0, len(node.Spec.Taints))
			found := false
			for _, current := range node.Spec.Taints {
				if current.Key == taint.Key {
					if current.Value != taint.Value || current.Effect != taint.Effect {
						return fmt.Errorf("test taint identity changed")
					}
					found = true
					continue
				}
				kept = append(kept, current)
			}
			if !found {
				return nil
			}
			node.Spec.Taints = kept
			_, err = cluster.CoreV1().Nodes().Update(cleanup, node, metav1.UpdateOptions{})
			return err
		})
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := remove(cleanup); err != nil {
			t.Errorf("remove dedicated test taint: %v", err)
			return
		}
		node, err := cluster.CoreV1().Nodes().Get(cleanup, "seraph", metav1.GetOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		writeEvidence("after", map[string]any{"node": node.Name, "uid": node.UID, "taints": node.Spec.Taints})
		if node.UID != original.UID || !reflect.DeepEqual(node.Spec.Taints, original.Spec.Taints) {
			t.Error("node identity or original taints differ after cleanup")
		}
		t.Logf("original node UID and complete taints restored; evidence %s", evidence)
	})
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := cluster.CoreV1().Nodes().Get(ctx, "seraph", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if node.UID != original.UID || !reflect.DeepEqual(node.Spec.Taints, original.Spec.Taints) {
			return fmt.Errorf("node identity or original taints changed before test")
		}
		node.Spec.Taints = append(node.Spec.Taints, taint)
		_, err = cluster.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	verify("tainted", false)
	if err = remove(ctx); err != nil {
		t.Fatal(err)
	}
	verify("restored", true)
}
