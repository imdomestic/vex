package rooms

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imdomestic/chorus-testbed/controller/internal/kube"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func TestReconcileErrorKeepsPersistedProgress(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := &Manager{DB: db}
	ctx := context.Background()
	node := "encore"
	room := model.Room{ID: "main-fabric", State: "starting", Phase: "creating server", Generation: 3, Node: &node}
	runtime := Runtime{Generation: 3, WorldExists: true}
	if err = manager.save(ctx, room, runtime); err != nil {
		t.Fatal(err)
	}
	if err = manager.recordReconcileError(ctx, room.ID, errors.New("Kubernetes operation unavailable")); err != nil {
		t.Fatal(err)
	}
	current, err := manager.Get(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := manager.runtime(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "starting" || current.Node == nil || *current.Node != node || current.Generation != 3 || !stored.WorldExists || !stored.ReconcileError || current.LastError == nil {
		t.Fatal("error reporting discarded actual persisted progress")
	}
}

func TestRoomDiagnosticsKeepCauseAndRemoveCredentials(t *testing.T) {
	runtime := Runtime{Spec: &kube.RoomSpec{RoomToken: "room-token-sensitive", RCONPassword: "rcon-password-sensitive"}}
	message := "Service ownership conflict: room-token-sensitive " + base64.StdEncoding.EncodeToString([]byte(runtime.Spec.RCONPassword)) + " https://controller/download?sig=signed-value&token=query-value Bearer header-value"
	redacted := roomDiagnostic(message, runtime)
	for _, secret := range []string{runtime.Spec.RoomToken, runtime.Spec.RCONPassword, "signed-value", "query-value", "header-value", base64.StdEncoding.EncodeToString([]byte(runtime.Spec.RCONPassword))} {
		if strings.Contains(redacted, secret) {
			t.Fatal("diagnostic exposed a credential")
		}
	}
	if !strings.Contains(redacted, "Service ownership conflict") {
		t.Fatal("diagnostic lost its failure cause")
	}
}
