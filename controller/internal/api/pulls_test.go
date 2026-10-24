package api

import (
	"encoding/json"
	"testing"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
)

func TestRoomListIncludesPullPreviewFilter(t *testing.T) {
	environment := newSecurityEnvironment(t)
	project := environment.projects["alpha"]
	project.Rooms.Cfg.CI.PRPreviews = true
	pull := model.Pull{Project: "alpha", Number: 12, HeadSHA: environment.sha, State: "open", Title: "预览房间"}
	value := model.Build{Project: "alpha", ID: rooms.BuildID(environment.sha, "fabric"), SHA: environment.sha, Loader: "fabric", Status: "ready"}
	if err := project.Rooms.SyncPull(t.Context(), pull, []model.Build{value}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := project.Rooms.Create(t.Context(), "commit", environment.older, "fabric", "empty", ""); err != nil {
		t.Fatal(err)
	}
	status, data := environment.request(t, "GET", "/v2/projects/alpha/rooms?kind=pr", environment.tokens["reader"], "")
	var result []model.Room
	if err := json.Unmarshal(data, &result); err != nil || status != 200 || len(result) != 1 || result[0].ID != "pr12-fabric" || result[0].Kind != "pr" || result[0].PR == nil || result[0].PR.Title != pull.Title {
		t.Fatalf("preview filter failed: status=%d rooms=%v error=%v", status, result, err)
	}
	environment.failure(t, "GET", "/v2/projects/alpha/rooms?kind=pr&kind=commit", environment.tokens["reader"], "", 400, "invalid_request")
}
