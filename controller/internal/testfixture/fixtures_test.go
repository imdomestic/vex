package testfixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryFixturesMatchTheirCatalog(t *testing.T) {
	var entries []entry
	if err := json.Unmarshal(catalog, &entries); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, item := range entries {
		if item.Location == "repository" {
			listed[item.Name] = true
			File(t, item.Name)
		}
	}
	files, err := repository.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(listed) {
		t.Fatal("testdata contains files missing from fixtures.json")
	}
	for _, file := range files {
		if !listed[file.Name()] {
			t.Fatalf("%s is not listed in fixtures.json", file.Name())
		}
	}
	world := WorldDirectory(t, "world-26.3.tar.gz", t.TempDir())
	if _, err = os.Stat(filepath.Join(world, "level.dat")); err != nil {
		t.Fatal("fixture world has no level.dat", err)
	}
}
