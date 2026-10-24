//go:build integration

package migration

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/filehash"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testfixture"
)

func TestRealWorldMigrationPreservesFilesCountersAndSeed(t *testing.T) {
	archive, version := testfixture.File(t, "world-26.3.tar.gz"), "26.3"
	for _, scenario := range []string{"apply", "source-changed", "resume-moved-world"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			from := filepath.Join(root, "old")
			to := filepath.Join(root, "new")
			filename := filepath.Join(from, "snapshots", "main-fabric", "main-fabric-g9.tar.gz")
			if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
				t.Fatal(err)
			}
			input, err := os.Open(archive)
			if err != nil {
				t.Fatal(err)
			}
			output, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				input.Close()
				t.Fatal(err)
			}
			_, err = io.Copy(output, input)
			input.Close()
			closeErr := output.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			hashes, err := filehash.File(filename)
			if err != nil {
				t.Fatal(err)
			}
			database := filepath.Join(from, "legacy.db")
			u := url.URL{Scheme: "file", Path: database}
			db, err := sql.Open("sqlite", u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("CREATE TABLE records(kind TEXT,id TEXT,data BLOB,PRIMARY KEY(kind,id)); CREATE TABLE counters(name TEXT PRIMARY KEY,value INTEGER NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			snapshot := model.Snapshot{ID: "main-fabric-g9", RoomID: "main-fabric", RoomGeneration: 9, Minecraft: version, SHA256: hashes.SHA256, Size: hashes.Size, LocalAvailable: true, TankVerifiedAt: &now}
			room := model.Room{ID: "main-fabric", Kind: "branch", State: "stopped", Generation: 9}
			for kind, object := range map[string]any{"snapshot": snapshot, "room": room} {
				data, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				id := room.ID
				if kind == "snapshot" {
					id = snapshot.ID
				}
				if _, err = db.Exec("INSERT INTO records(kind,id,data) VALUES(?,?,?)", kind, id, data); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.Exec("INSERT INTO counters(name,value) VALUES('asset_revision:fixture',3)"); err != nil {
				t.Fatal(err)
			}
			options := Options{FromDB: database, FromDir: from, ToDir: to, Project: "legacy", Zone: "vex.internal"}
			plan, err := Prepare(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filename); err != nil {
				t.Fatal("dry-run changed source file", err)
			}
			if _, err = os.Stat(to); !os.IsNotExist(err) {
				t.Fatal("dry-run created destination")
			}
			if scenario == "source-changed" {
				if _, err = db.Exec("UPDATE counters SET value=4"); err != nil {
					t.Fatal(err)
				}
				if err = Apply(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "changed after planning") {
					t.Fatal("changed database was not rejected", err)
				}
				if _, err = os.Stat(filename); err != nil {
					t.Fatal("rejected migration moved source", err)
				}
				return
			}
			if scenario == "resume-moved-world" {
				value := readyJournal(t, plan)
				if err = moveDirectory(&value, 0); err != nil {
					t.Fatal(err)
				}
				_, err = Resume(t.Context(), JournalPath(plan))
			} else {
				err = Apply(t.Context(), plan)
			}
			if err != nil {
				t.Fatal(err)
			}
			migrated, err := store.Open(plan.DestinationDB)
			if err != nil {
				t.Fatal(err)
			}
			defer migrated.Close()
			project, err := migrated.WithProject("legacy")
			if err != nil {
				t.Fatal(err)
			}
			if err = project.Get(t.Context(), "room", room.ID, &room); err != nil {
				t.Fatal(err)
			}
			if room.Seed != "snapshot:main-fabric-g9" || room.Host != "main-fabric--legacy.vex.internal" || room.Generation != 9 || room.Project != "legacy" || room.State != "stopped" {
				t.Fatal("main seed or identity was not preserved")
			}
			if err = project.Get(t.Context(), "snapshot", snapshot.ID, &snapshot); err != nil {
				t.Fatal(err)
			}
			result, err := filehash.File(filepath.Join(to, "projects", "legacy", snapshot.Path))
			if err != nil || result != hashes || snapshot.SHA1 != hashes.SHA1 || snapshot.SHA512 != hashes.SHA512 || snapshot.TankVerifiedAt == nil {
				t.Fatal("snapshot content or verification changed", err)
			}
			if err = project.Update(t.Context(), func(tx *store.Tx) error {
				next, err := tx.Next(t.Context(), "world_revision:fixture")
				if next != 4 {
					t.Errorf("revision counter was not preserved: %d", next)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err = Apply(t.Context(), plan); err == nil {
				t.Fatal("repeat migration accepted an existing destination")
			}
		})
	}
}
