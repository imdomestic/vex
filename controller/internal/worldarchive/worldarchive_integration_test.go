//go:build integration

package worldarchive

import (
	"testing"

	"github.com/imdomestic/vex/controller/internal/testfixture"
)

func TestActualStoppedWorld(t *testing.T) {
	for _, name := range []string{"world-26.3.tar.gz", "world-26.3-large.tar.gz"} {
		if err := Validate(testfixture.File(t, name), "26.3"); err != nil {
			t.Fatal(name, err)
		}
	}
}
