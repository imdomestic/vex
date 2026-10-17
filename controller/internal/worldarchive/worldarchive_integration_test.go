//go:build integration

package worldarchive

import (
	"os"
	"testing"
)

func TestActualStoppedWorld(t *testing.T) {
	filename, version := os.Getenv("VEX_IT_WORLD_ARCHIVE"), os.Getenv("VEX_IT_MINECRAFT")
	if filename == "" || version == "" {
		t.Skip("requires VEX_IT_WORLD_ARCHIVE and VEX_IT_MINECRAFT from an actual stopped world")
	}
	if err := Validate(filename, version); err != nil {
		t.Fatal(err)
	}
}
