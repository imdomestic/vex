package rooms

import (
	"errors"
	"testing"

	"github.com/imdomestic/vex/controller/internal/auth"
	buildmetadata "github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestPullRegistrationTracksMetadataAndKeepsActiveBuild(t *testing.T) {
	m, source, sha := freshnessManager(t)
	minecraft := "26.3"
	m.Cfg.CI = config.ProjectCI{PRPreviews: true, PreviewAccess: "members", ForkPreviews: "approval"}
	pull := model.Pull{Project: m.Cfg.ID, Number: 7, Title: "预览内容", Author: "contributor", URL: "https://github.com/example/project/pull/7", HeadSHA: sha, State: "open"}
	if err := m.SyncPull(t.Context(), pull, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(t.Context(), "pr7-fabric"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("PR without a ready build registered a room", err)
	}
	build := model.Build{Project: m.Cfg.ID, ID: BuildID(sha, "fabric"), SHA: sha, Loader: "fabric", Status: "ready", Minecraft: &minecraft}
	completePreviewMetadata(t, &build)
	if err := m.DB.Put(t.Context(), "build", build.ID, build); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncPull(t.Context(), pull, []model.Build{build}); err != nil {
		t.Fatal(err)
	}
	room, err := m.Get(t.Context(), "pr7-fabric")
	if err != nil || room.State != "stopped" || room.Generation != 0 || room.PR == nil || room.PR.HeadSHA != sha || room.PR.Title != pull.Title {
		t.Fatal("ready PR registration did not preserve lazy state and metadata", err)
	}
	nextSHA := freshnessCommit(t, source, "预览提交更新")
	if err = m.Git.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := model.Build{Project: m.Cfg.ID, ID: BuildID(nextSHA, "fabric"), SHA: nextSHA, Loader: "fabric", Status: "ready", Minecraft: &minecraft}
	completePreviewMetadata(t, &next)
	if err = m.DB.Put(t.Context(), "build", next.ID, next); err != nil {
		t.Fatal(err)
	}
	pull.HeadSHA, pull.Title = nextSHA, "新的预览标题"
	if err = m.SyncPull(t.Context(), pull, []model.Build{next}); err != nil {
		t.Fatal(err)
	}
	packed, target, err := m.PackTarget(t.Context(), room.ID)
	if err != nil || packed.Generation != 0 || target.ID != next.ID || packed.BuildID != next.ID || packed.PR.Title != pull.Title {
		t.Fatal("stopped PR pack did not select the ready head", err)
	}
	started, _, err := m.Start(t.Context(), room.ID, nil, auth.Identity{User: "integration"})
	if err != nil || started.Room.Generation != 1 || started.Room.BuildID != next.ID {
		t.Fatal("PR start did not accept the latest ready build", err)
	}
	if started.Room.Commit == nil || started.Room.Commit.SHA != nextSHA || started.Room.Commit.Subject != "预览提交更新" {
		t.Fatal("PR room did not record the metadata of its accepted commit", started.Room.Commit)
	}
	thirdSHA := freshnessCommit(t, source, "运行期间新增提交")
	if err = m.Git.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	third := model.Build{Project: m.Cfg.ID, ID: BuildID(thirdSHA, "fabric"), SHA: thirdSHA, Loader: "fabric", Status: "ready", Minecraft: &minecraft}
	completePreviewMetadata(t, &third)
	if err = m.DB.Put(t.Context(), "build", third.ID, third); err != nil {
		t.Fatal(err)
	}
	pull.HeadSHA, pull.Title = thirdSHA, "运行期间标题"
	if err = m.SyncPull(t.Context(), pull, []model.Build{third}); err != nil {
		t.Fatal(err)
	}
	active, target, err := m.PackTarget(t.Context(), room.ID)
	if err != nil || active.Generation != 1 || active.BuildID != next.ID || target.ID != next.ID || active.PR.HeadSHA != thirdSHA || !active.BehindHead {
		t.Fatal("PR metadata update replaced an active build or omitted its new head", err)
	}
	rt, err := m.runtime(t.Context(), room.ID)
	if err != nil || rt.PendingSHA != thirdSHA {
		t.Fatal("pending PR head was not persisted", err)
	}
	pull.State = "closed"
	if err = m.SyncPull(t.Context(), pull, nil); err != nil {
		t.Fatal(err)
	}
	closed, err := m.Get(t.Context(), room.ID)
	if err != nil || closed.State != "stopping" || closed.Label != "finished" {
		t.Fatal("closing an accepted generation skipped normal stopping", err)
	}
	rt, err = m.runtime(t.Context(), room.ID)
	if err != nil || rt.Restart || !rt.PullClosed || rt.PendingSHA != "" {
		t.Fatal("closed PR retained a pending restart", err)
	}
}

func TestPullApprovalAndProjectBoundaries(t *testing.T) {
	m, _, sha := freshnessManager(t)
	m.Cfg.CI = config.ProjectCI{PRPreviews: true, PreviewAccess: "members", ForkPreviews: "approval"}
	pull := model.Pull{Project: m.Cfg.ID, Number: 9, HeadSHA: sha, State: "open", Fork: true}
	build := model.Build{Project: m.Cfg.ID, ID: BuildID(sha, "fabric"), SHA: sha, Loader: "fabric", Status: "ready"}
	if err := m.DB.Put(t.Context(), "build", build.ID, build); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncPull(t.Context(), pull, []model.Build{build}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(t.Context(), "pr9-fabric"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("unapproved fork registered a preview room", err)
	}
	pull.Approved = true
	if err := m.SyncPull(t.Context(), pull, []model.Build{build}); err != nil {
		t.Fatal(err)
	}
	pull.State = "closed"
	if err := m.SyncPull(t.Context(), pull, nil); err != nil {
		t.Fatal(err)
	}
	room, err := m.Get(t.Context(), "pr9-fabric")
	if err != nil || room.State != "closed" || room.Generation != 0 || room.ClosedAt == nil {
		t.Fatal("unused closed PR has an invalid lifecycle", err)
	}
	pull.State = "open"
	if err := m.SyncPull(t.Context(), pull, []model.Build{build}); err != nil {
		t.Fatal(err)
	}
	room, err = m.Get(t.Context(), room.ID)
	if err != nil || room.State != "stopped" || room.Label != "testing" || room.ClosedAt != nil {
		t.Fatal("reopened approved PR remained closed", err)
	}
	pull.Project = "another"
	if err := m.SyncPull(t.Context(), pull, []model.Build{build}); err == nil {
		t.Fatal("accepted another project's pull")
	}
}

func TestRetainedPreviewWorldStaysOnItsOriginalNode(t *testing.T) {
	m, _, sha := freshnessManager(t)
	m.Cfg.Quota.MaxRooms = 2
	room := m.newRoom("pr12-fabric", "pr", sha, "fabric", "empty")
	node := "original"
	room.Node = &node
	room.Generation = 1
	if err := m.save(t.Context(), room, Runtime{Generation: 1, WorldExists: true}); err != nil {
		t.Fatal(err)
	}
	nodes := []model.Node{
		{Name: "preferred", Ready: true, Schedulable: true, Roles: []string{"ephemeral"}, Priority: 100, MaxRunning: 2},
		{Name: node, Ready: true, Schedulable: true, Roles: []string{"ephemeral"}, Priority: 1, MaxRunning: 2},
	}
	selected, err := m.chooseNode(t.Context(), room, nodes)
	if err != nil || selected != node {
		t.Fatal("retained preview world moved to another node", err)
	}
	nodes[1].Schedulable = false
	selected, err = m.chooseNode(t.Context(), room, nodes)
	if err != nil || selected != "" {
		t.Fatal("retained world ignored its original node scheduling restriction", err)
	}
}

func completePreviewMetadata(t *testing.T, value *model.Build) {
	t.Helper()
	java, loader := 25, "0.19.5"
	value.Java, value.LoaderVersion = &java, &loader
	value.RoomMemory, value.WorldExtraRootEntries = "4G", []string{}
	var err error
	value.ClientFingerprint, err = buildmetadata.ClientFingerprint(*value)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPreviewRejectsUninitializedBuildWithoutAcceptingGeneration(t *testing.T) {
	m, _, sha := freshnessManager(t)
	m.Cfg.CI = config.ProjectCI{PRPreviews: true, PreviewAccess: "members"}
	value := model.Build{Project: m.Cfg.ID, ID: BuildID(sha, "fabric"), SHA: sha, Loader: "fabric", Status: "ready"}
	if err := m.DB.Put(t.Context(), "build", value.ID, value); err != nil {
		t.Fatal(err)
	}
	pull := model.Pull{Project: m.Cfg.ID, Number: 19, HeadSHA: sha, State: "open"}
	if err := m.SyncPull(t.Context(), pull, []model.Build{value}); err != nil {
		t.Fatal(err)
	}
	_, _, packErr := m.PackTarget(t.Context(), "pr19-fabric")
	_, _, startErr := m.Start(t.Context(), "pr19-fabric", nil, auth.Identity{User: "integration"})
	for _, err := range []error{packErr, startErr} {
		var failure *model.Error
		if !errors.As(err, &failure) || failure.Code != "build_mismatch" {
			t.Fatal("uninitialized build was accepted", err)
		}
	}
	room, err := m.Get(t.Context(), "pr19-fabric")
	if err != nil || room.Generation != 0 || room.State != "stopped" {
		t.Fatal("uninitialized build changed the accepted generation", err)
	}
}
