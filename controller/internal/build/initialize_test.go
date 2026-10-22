package build

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/model"
)

func TestBackfillRealPublishedBuildPreservesFilesAndIdentity(t *testing.T) {
	m, sha, _, _, archive := realBundle(t)
	values, _, err := m.ImportCI(t.Context(), CIImport{SHA: sha, RunID: 200, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, BundlePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, value := range values {
		if value.Commit == nil || value.Commit.SHA != sha || value.Commit.Subject != "declare runtime artifacts" || value.Manifest == nil || value.Manifest.Minecraft.Version != "26.3" {
			t.Fatalf("imported build lacks its commit metadata or manifest: %+v", value)
		}
		filename := filepath.Join(m.attemptDir(value), "manifest.json")
		before[filename], err = os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		legacy := value
		legacy.ClientFingerprint, legacy.RoomMemory, legacy.WorldExtraRootEntries, legacy.Commit, legacy.Manifest = "", "", nil, nil, nil
		if err = m.db.Put(t.Context(), "build", legacy.ID, legacy); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		issues, err := m.Backfill(t.Context())
		if err != nil || len(issues) != 0 {
			t.Fatal(issues, err)
		}
	}
	for _, expected := range values {
		var actual model.Build
		if err = m.db.Get(t.Context(), "build", expected.ID, &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(expected, actual) {
			t.Fatalf("build metadata or identity changed: %+v %+v", expected, actual)
		}
		for _, file := range actual.Files {
			opened, err := m.OpenFile(t.Context(), actual.ID, file.Name)
			if err != nil {
				t.Fatal(err)
			}
			if err = opened.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for filename, original := range before {
		actual, err := os.ReadFile(filename)
		if err != nil || !bytes.Equal(original, actual) {
			t.Fatal("published manifest file changed", err)
		}
	}
	wrong := values[0]
	wrong.ClientFingerprint = "incorrect"
	if err = m.db.Put(t.Context(), "build", wrong.ID, wrong); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Backfill(t.Context()); err == nil {
		t.Fatal("inconsistent metadata accepted")
	}
	missing := values[0]
	missing.WorldExtraRootEntries = nil
	if err = ValidateMetadata(missing); err == nil {
		t.Fatal("missing world root policy accepted")
	}
}

func TestBackfillListsUnreadableBuildsAndKeepsCompleteBuildsAvailable(t *testing.T) {
	m, sha, _, _, archive := realBundle(t)
	values, _, err := m.ImportCI(t.Context(), CIImport{SHA: sha, RunID: 201, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, BundlePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(filepath.Dir(m.cfg.Git.Remote), "mirror.git")
	if err = os.Rename(mirror, mirror+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if issues, err := m.Backfill(t.Context()); err != nil || len(issues) != 0 {
		t.Fatal("complete builds depend on Git mirror", issues, err)
	}
	// 只缺少提交元数据的旧记录仍可启动房间；缺少清单信息的旧记录要等补录。
	older := values[1]
	older.Commit, older.Manifest = nil, nil
	legacy := values[0]
	legacy.ClientFingerprint, legacy.RoomMemory, legacy.WorldExtraRootEntries, legacy.Commit, legacy.Manifest = "", "", nil, nil, nil
	for _, value := range []model.Build{legacy, older} {
		if err = m.db.Put(t.Context(), "build", value.ID, value); err != nil {
			t.Fatal(err)
		}
	}
	issues, err := m.Backfill(t.Context())
	if err != nil || len(issues) != 2 {
		t.Fatal("unreadable historical commits were not listed", issues, err)
	}
	for _, issue := range issues {
		if issue.Kind != "build" || issue.SHA != sha || issue.Reason != "commit_unavailable" {
			t.Fatalf("unexpected backfill issue: %+v", issue)
		}
	}
	if err = ValidateMetadata(legacy); err == nil {
		t.Fatal("incomplete build accepted")
	}
	if err = ValidateMetadata(older); err != nil {
		t.Fatal("complete build without commit metadata unavailable", err)
	}
	if err = os.Rename(mirror+".unavailable", mirror); err != nil {
		t.Fatal(err)
	}
	if issues, err = m.Backfill(t.Context()); err != nil || len(issues) != 0 {
		t.Fatal("backfill retry failed", issues, err)
	}
	for _, expected := range values {
		var restored model.Build
		if err = m.db.Get(t.Context(), "build", expected.ID, &restored); err != nil || !reflect.DeepEqual(expected, restored) {
			t.Fatal("retry did not restore metadata", err)
		}
	}
}

func TestBackfillCompletesOtherRecordsWhenOneCommitIsUnavailable(t *testing.T) {
	m, sha, _, _, archive := realBundle(t)
	values, _, err := m.ImportCI(t.Context(), CIImport{SHA: sha, RunID: 202, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, BundlePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range values {
		value.ClientFingerprint, value.RoomMemory, value.WorldExtraRootEntries, value.Commit, value.Manifest = "", "", nil, nil, nil
		if index == 0 {
			value.SHA = strings.Repeat("a", 40)
		}
		if err = m.db.Put(t.Context(), "build", value.ID, value); err != nil {
			t.Fatal(err)
		}
	}
	issues, err := m.Backfill(t.Context())
	if err != nil || len(issues) != 1 || issues[0].ID != values[0].ID || issues[0].Reason != "commit_unavailable" {
		t.Fatal("missing commit was not identified", issues, err)
	}
	var available model.Build
	if err = m.db.Get(t.Context(), "build", values[1].ID, &available); err != nil || !reflect.DeepEqual(available, values[1]) {
		t.Fatal("independent valid record was not initialized", err)
	}
}
