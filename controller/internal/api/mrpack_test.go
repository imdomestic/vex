package api

import (
	"net/url"
	"testing"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
)

func TestRoomMRPackRejectsChangedJoinTargetBeforeReadingFiles(t *testing.T) {
	environment := newSecurityEnvironment(t)
	project := environment.projects["alpha"]
	build := model.Build{Project: "alpha", ID: rooms.BuildID(environment.sha, "fabric"), SHA: environment.sha, Loader: "fabric", Status: "ready"}
	if err := project.DB.Put(t.Context(), "build", build.ID, build); err != nil {
		t.Fatal(err)
	}
	room := model.Room{Project: "alpha", ID: "main-fabric", Kind: "branch", Generation: 3, SHA: build.SHA, Loader: build.Loader, BuildID: build.ID, State: "starting"}
	if err := project.DB.Put(t.Context(), "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	base := "/v2/projects/alpha/rooms/main-fabric/pack.mrpack"
	token := environment.tokens["reader"]
	for _, query := range []string{
		"buildId=" + url.QueryEscape(build.ID),
		"generation=3",
		"buildId=" + url.QueryEscape(build.ID) + "&generation=0",
		"buildId=" + url.QueryEscape(build.ID) + "&generation=3&generation=4",
		"buildId=" + url.QueryEscape(build.ID) + "&buildId=other&generation=3",
	} {
		environment.failure(t, "GET", base+"?"+query, token, "", 400, "invalid_request")
	}
	for _, query := range []string{
		"buildId=" + url.QueryEscape(build.ID) + "&generation=2",
		"buildId=" + url.QueryEscape(rooms.BuildID(environment.older, "fabric")) + "&generation=3",
	} {
		environment.failure(t, "GET", base+"?"+query, token, "", 409, "build_mismatch")
	}
	var retained model.Room
	if err := project.DB.Get(t.Context(), "room", room.ID, &retained); err != nil || retained.BuildID != build.ID || retained.Generation != 3 || retained.State != "starting" {
		t.Fatal("failed download changed the accepted join target", err)
	}
}
