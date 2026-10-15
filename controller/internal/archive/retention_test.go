package archive

import (
	"fmt"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

func TestSnapshotRetentionProtectsRecentUnverifiedAndSeedReferences(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	verified := now.Add(-30 * 24 * time.Hour)
	snapshots := []model.Snapshot{}
	for index := 0; index < 9; index++ {
		snapshots = append(snapshots, model.Snapshot{ID: fmt.Sprintf("snapshot-%d", index), RoomID: "main-fabric", CapturedAt: now.Add(-time.Duration(20+index) * 24 * time.Hour), TankVerifiedAt: &verified})
	}
	snapshots[6].TankVerifiedAt = nil
	snapshots[7].Restore = &model.Restore{Status: "running"}
	snapshots = append(snapshots, model.Snapshot{ID: "recent", RoomID: "old-branch", CapturedAt: now.Add(-2 * 24 * time.Hour), TankVerifiedAt: &verified})
	rooms := []model.Room{{ID: "seeded", State: "queued", SeedResolved: &model.SeedResolved{Kind: "snapshot", ID: "snapshot-8"}}}
	retained := RetainedSnapshots(snapshots, rooms, 5, 14, now)
	for _, id := range []string{"snapshot-0", "snapshot-1", "snapshot-2", "snapshot-3", "snapshot-4", "snapshot-6", "snapshot-7", "snapshot-8", "recent"} {
		if !retained[id] {
			t.Errorf("required snapshot %s was evicted", id)
		}
	}
	if retained["snapshot-5"] {
		t.Fatal("old verified unreferenced snapshot was retained")
	}
	rooms[0].State = "closed"
	if RetainedSnapshots(snapshots, rooms, 5, 14, now)["snapshot-8"] {
		t.Fatal("closed room incorrectly protects its old seed")
	}
}

func TestArchivePathBoundaries(t *testing.T) {
	for _, value := range []string{"", ".", "..", "../outside", "/absolute", "assets/../../outside", "assets\\outside", "assets/line\nname"} {
		if safeRelative(value) {
			t.Errorf("accepted unsafe relative path %q", value)
		}
	}
	if !safeRelative("assets/plaza/1/world.tar.gz") {
		t.Fatal("valid relative archive rejected")
	}
	for _, value := range []string{"tank", "-host:/path", "tank:relative", "tank:/root/../outside", "tank:/root\nnext"} {
		if _, _, err := target(value); err == nil {
			t.Errorf("accepted invalid SSH target %q", value)
		}
	}
	host, root, err := target("linwhite@tank:/srv/archive/chorus/it-20261015")
	if err != nil || host != "linwhite@tank" || root != "/srv/archive/chorus/it-20261015" {
		t.Fatal(host, root, err)
	}
}
