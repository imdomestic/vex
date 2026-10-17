package main

import (
	"strings"
	"testing"
)

func TestMigrationResumeRejectsAdditionalOptions(t *testing.T) {
	for _, option := range []string{"--dry-run", "--project=example", "--to-dir=/srv/vex", "--from-db=/srv/legacy.db"} {
		err := migrateCommand(t.Context(), []string{"chorus", "--resume=/srv/vex/state/migration-example.json", option})
		if err == nil || !strings.Contains(err.Error(), "must be used alone") {
			t.Fatal("resume accepted additional migration options", err)
		}
	}
}
