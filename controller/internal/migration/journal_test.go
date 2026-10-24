package migration

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func journalFixture(t *testing.T) Plan {
	t.Helper()
	root := t.TempDir()
	plan := Plan{Project: "example", SourceDir: filepath.Join(root, "source"), DestinationDB: filepath.Join(root, "destination", "state", "vexd.db"), SourceHashes: map[string]string{}, Counters: map[string]int64{"world_revision:spawn": 3}}
	plan.SourceDB = filepath.Join(plan.SourceDir, "legacy.db")
	for _, pair := range [][2]string{{"assets", "worlds"}, {"packs", "packs"}, {"gradle-home", "cache/gradle"}} {
		from := filepath.Join(plan.SourceDir, pair[0])
		if err := os.MkdirAll(from, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(from, "metadata.json"), []byte(`{"source":"filesystem-integration","revision":3}`), 0600); err != nil {
			t.Fatal(err)
		}
		identity, err := fileIdentity(from, true)
		if err != nil {
			t.Fatal(err)
		}
		plan.Moves = append(plan.Moves, Move{From: from, To: filepath.Join(root, "destination", "projects", plan.Project, pair[1]), Identity: identity})
	}
	db, err := sql.Open("sqlite", plan.SourceDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE records(kind TEXT,id TEXT,data BLOB,PRIMARY KEY(kind,id)); CREATE TABLE counters(name TEXT PRIMARY KEY,value INTEGER NOT NULL); INSERT INTO counters VALUES('asset_revision:spawn',3)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	plan.Records = []Record{{Kind: "room_tombstone", ID: "old-room", Data: json.RawMessage(`{"generation":2}`)}}
	plan.SourceState, err = fingerprintFile(t.Context(), plan.SourceDB)
	if err != nil {
		t.Fatal(err)
	}
	if err = collectSourceHashes(&plan); err != nil {
		t.Fatal(err)
	}
	plan.SHA256, err = planDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func startJournal(t *testing.T, plan Plan) journal {
	t.Helper()
	if err := makeDurableDirectory(filepath.Dir(plan.DestinationDB)); err != nil {
		t.Fatal(err)
	}
	value, err := beginMigration(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func readyJournal(t *testing.T, plan Plan) journal {
	t.Helper()
	value := startJournal(t, plan)
	if err := prepareDatabase(t.Context(), &value); err != nil {
		t.Fatal(err)
	}
	value.DatabaseReady = true
	if err := writeJournal(JournalPath(plan), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestResumeFilesystemAndSQLiteBoundaries(t *testing.T) {
	for _, boundary := range []string{"empty-database", "database-committed", "directory-renamed", "directory-recorded", "database-renamed", "complete"} {
		t.Run(boundary, func(t *testing.T) {
			plan := journalFixture(t)
			var value journal
			if boundary == "empty-database" || boundary == "database-committed" {
				value = startJournal(t, plan)
				if boundary == "database-committed" {
					if err := prepareDatabase(t.Context(), &value); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				value = readyJournal(t, plan)
				if boundary == "directory-renamed" {
					if err := renameExclusive(plan.Moves[0].From, plan.Moves[0].To); err != nil {
						t.Fatal(err)
					}
				} else if boundary == "directory-recorded" {
					if err := moveDirectory(&value, 0); err != nil {
						t.Fatal(err)
					}
				} else {
					for index := range plan.Moves {
						if err := moveDirectory(&value, index); err != nil {
							t.Fatal(err)
						}
					}
					if boundary == "database-renamed" {
						if err := renameExclusive(value.Stage, plan.DestinationDB); err != nil {
							t.Fatal(err)
						}
					} else if err := advanceMigration(t.Context(), &value); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := Resume(t.Context(), JournalPath(plan))
			if err != nil || result.SHA256 != plan.SHA256 {
				t.Fatal("resume failed", err)
			}
			final, err := readJournal(JournalPath(plan))
			if err != nil || !final.Complete {
				t.Fatal("completion was not persisted", err)
			}
			if _, err = verifyProgress(&final); err != nil {
				t.Fatal(err)
			}
			if fingerprint, err := fingerprintFile(t.Context(), plan.DestinationDB); err != nil || fingerprint != final.TargetSHA256 {
				t.Fatal("destination content differs", err)
			}
		})
	}
}

func TestResumeRejectsChangedStateWithoutFurtherMoves(t *testing.T) {
	for _, changed := range []string{"source-database", "moved-file", "additional-file", "missing-file", "mixed-directory", "source-recreated", "stage-database", "foreign-database", "journal", "symlink-parent"} {
		t.Run(changed, func(t *testing.T) {
			plan := journalFixture(t)
			value := readyJournal(t, plan)
			if err := moveDirectory(&value, 0); err != nil {
				t.Fatal(err)
			}
			var err error
			switch changed {
			case "source-database", "stage-database":
				filename := plan.SourceDB
				if changed == "stage-database" {
					filename = value.Stage
				}
				db, openErr := sql.Open("sqlite", filename)
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, err = db.Exec("UPDATE counters SET value=value+1")
				db.Close()
			case "moved-file":
				err = os.WriteFile(filepath.Join(plan.Moves[0].To, "metadata.json"), []byte(`{"revision":4}`), 0600)
			case "additional-file":
				err = os.WriteFile(filepath.Join(plan.Moves[0].To, "extra.json"), []byte(`{}`), 0600)
			case "missing-file":
				err = os.Remove(filepath.Join(plan.Moves[0].To, "metadata.json"))
			case "mixed-directory":
				err = os.Mkdir(plan.Moves[1].To, 0700)
			case "source-recreated":
				err = os.Mkdir(plan.Moves[0].From, 0700)
			case "foreign-database":
				err = os.WriteFile(plan.DestinationDB, []byte("operator-owned-file"), 0600)
			case "journal":
				value.Plan.Counters["world_revision:spawn"]++
				data, marshalErr := json.Marshal(value)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = os.WriteFile(JournalPath(plan), data, 0600)
			case "symlink-parent":
				parent := filepath.Dir(plan.Moves[2].To)
				err = os.Rename(parent, parent+"-actual")
				if err == nil {
					err = os.Symlink(parent+"-actual", parent)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Resume(t.Context(), JournalPath(plan)); err == nil {
				t.Fatal("changed migration accepted")
			}
			if _, err = os.Stat(plan.Moves[1].From); err != nil {
				t.Fatal("failed resume moved remaining source", err)
			}
			if _, err = os.Stat(plan.Moves[0].To); err != nil {
				t.Fatal("failed resume reversed completed move", err)
			}
		})
	}
}

func TestMigrationRejectsPlanChangesAliasesAndReplacement(t *testing.T) {
	plan := journalFixture(t)
	plan.Counters["world_revision:spawn"]++
	if err := Apply(t.Context(), plan); err == nil {
		t.Fatal("modified plan accepted")
	}
	alias := filepath.Join(filepath.Dir(plan.SourceDir), "alias")
	if err := os.Symlink(plan.SourceDir, alias); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(t.Context(), Options{FromDB: plan.SourceDB, FromDir: plan.SourceDir, ToDir: filepath.Join(alias, "new"), Project: "example", Zone: "vex.internal"})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatal("aliased overlapping destination accepted", err)
	}
	if err = os.Mkdir(plan.Moves[0].From+"-occupied", 0700); err != nil {
		t.Fatal(err)
	}
	if err = renameExclusive(plan.Moves[0].From, plan.Moves[0].From+"-occupied"); err == nil {
		t.Fatal("exclusive rename replaced an existing empty directory")
	}
}

func TestMigrationLocksExcludeAnotherOperator(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "migration.lock")
	lock, err := acquireMigrationLock(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	other, err := acquireMigrationLock(filename)
	if err == nil {
		other.Close()
		t.Fatal("another migration acquired the same lock")
	}
}
