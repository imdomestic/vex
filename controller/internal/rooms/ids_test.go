package rooms

import (
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/model"
)

func TestIDsAndStartTransitions(t *testing.T) {
	sha := strings.Repeat("a", 40)
	if id, err := RoomID("commit", sha, "quilt", ""); err != nil || id != "caaaaaaaa-quilt" {
		t.Fatal("room ID must accept configured loader IDs", id, err)
	}
	for _, loader := range []string{"", "Fabric", "bad--loader", "../fabric"} {
		if _, err := RoomID("commit", sha, loader, ""); err == nil {
			t.Fatal("invalid loader ID was accepted", loader)
		}
	}
	if id := BranchID("test", "Feature/Hello_World", "fabric", false); id != "feature-hello-world-fabric" {
		t.Fatal(id)
	}
	project := strings.Repeat("p", 23)
	long := BranchID(project, strings.Repeat("branch", 20), "fabric", false)
	if len(long)+2+len(project) > 63 || !strings.HasSuffix(long, "-fabric") {
		t.Fatal(long)
	}
	if BranchID("test", "a/b", "fabric", true) == BranchID("test", "a_b", "fabric", true) {
		t.Fatal("collision suffix is not tied to original branch")
	}
	if !strings.HasPrefix(BranchID("test", "中文", "fabric", false), "branch-") {
		t.Fatal("empty slug was not named")
	}
	for _, state := range []string{"stopped", "closed", "failed", "queued", "building", "starting", "ready"} {
		if err := CanStart(model.Room{State: state, Label: "testing"}); err != nil {
			t.Fatal(state, err)
		}
	}
	for _, state := range []string{"stopping", "archiving", "lost"} {
		if CanStart(model.Room{State: state, Label: "testing"}) == nil {
			t.Fatal(state)
		}
	}
	if CanStart(model.Room{State: "ready", Label: "finished"}) == nil {
		t.Fatal("finished room can start")
	}
}
