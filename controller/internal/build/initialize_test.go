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

func TestInitializeRealPublishedBuildPreservesFilesAndIdentity(t *testing.T) {
	m, sha, _, _, archive := realBundle(t)
	values, _, err := m.ImportCI(t.Context(), CIImport{SHA: sha, RunID: 200, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, BundlePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, value := range values {
		filename := filepath.Join(m.attemptDir(value), "manifest.json")
		before[filename], err = os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		legacy := value
		legacy.ClientFingerprint, legacy.RoomMemory, legacy.WorldExtraRootEntries = "", "", nil
		if err = m.db.Put(t.Context(), "build", legacy.ID, legacy); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err = m.InitializeRecords(t.Context()); err != nil {
			t.Fatal(err)
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
	if err = m.InitializeRecords(t.Context()); err == nil {
		t.Fatal("inconsistent metadata accepted")
	}
	missing := values[0]
	missing.WorldExtraRootEntries = nil
	if err = ValidateMetadata(missing); err == nil {
		t.Fatal("missing world root policy accepted")
	}
}

func TestInitializationKeepsCompleteBuildsAvailableWithoutMirror(t *testing.T) {
	m, sha, _, _, archive := realBundle(t)
	values, _, err := m.ImportCI(t.Context(), CIImport{SHA: sha, RunID: 201, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, BundlePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(filepath.Dir(m.cfg.Git.Remote), "mirror.git")
	if err = os.Rename(mirror, mirror+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if err = m.InitializeRecords(t.Context()); err != nil {
		t.Fatal("complete builds depend on Git mirror", err)
	}
	legacy := values[0]
	legacy.ClientFingerprint, legacy.RoomMemory, legacy.WorldExtraRootEntries = "", "", nil
	if err = m.db.Put(t.Context(), "build", legacy.ID, legacy); err != nil {
		t.Fatal(err)
	}
	if err = m.InitializeRecords(t.Context()); err == nil || !strings.Contains(err.Error(), legacy.ID) {
		t.Fatal("missing historical manifest was not reported", err)
	}
	if err = ValidateMetadata(legacy); err == nil {
		t.Fatal("incomplete build accepted")
	}
	if err = ValidateMetadata(values[1]); err != nil {
		t.Fatal("complete build unavailable", err)
	}
	if err = os.Rename(mirror+".unavailable", mirror); err != nil {
		t.Fatal(err)
	}
	if err = m.InitializeRecords(t.Context()); err != nil {
		t.Fatal("initialization retry failed", err)
	}
	var restored model.Build
	if err = m.db.Get(t.Context(), "build", legacy.ID, &restored); err != nil || !reflect.DeepEqual(values[0], restored) {
		t.Fatal("retry did not restore metadata", err)
	}
}

func TestInitializationCompletesOtherRecordsWhenOneCommitIsUnavailable(t *testing.T) {
	m, sha, _, _, archive := realBundle(t)
	values, _, err := m.ImportCI(t.Context(), CIImport{SHA: sha, RunID: 202, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, BundlePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range values {
		value.ClientFingerprint, value.RoomMemory, value.WorldExtraRootEntries = "", "", nil
		if index == 0 {
			value.SHA = strings.Repeat("a", 40)
		}
		if err = m.db.Put(t.Context(), "build", value.ID, value); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.InitializeRecords(t.Context()); err == nil || !strings.Contains(err.Error(), values[0].ID) {
		t.Fatal("missing commit was not identified", err)
	}
	var available model.Build
	if err = m.db.Get(t.Context(), "build", values[1].ID, &available); err != nil || !reflect.DeepEqual(available, values[1]) {
		t.Fatal("independent valid record was not initialized", err)
	}
}
