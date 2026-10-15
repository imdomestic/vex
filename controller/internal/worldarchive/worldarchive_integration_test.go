//go:build integration

package worldarchive

import (
	"os"
	"testing"
)

func TestActualStoppedChorusWorld(t *testing.T) {
	filename := os.Getenv("CHORUS_IT_WORLD_ARCHIVE")
	if filename == "" {
		t.Skip("requires an actual stopped Chorus world archive")
	}
	if err := Validate(filename, "26.3"); err != nil {
		t.Fatal(err)
	}
}
