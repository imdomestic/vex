package build

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedBundleFormatVector(t *testing.T) {
	data, err := os.ReadFile("testdata/vex-build.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector Bundle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&vector); err != nil {
		t.Fatal(err)
	}
	if err = vector.validate(); err != nil {
		t.Fatal(err)
	}
	if vector.Minecraft != "26.3" || vector.Java != 25 || len(vector.Loaders) != 1 || vector.Loaders["fabric"].Files[0].Size != 3453 {
		t.Fatal("shared vector changed")
	}
}

func TestSharedBundleMatchesActionSource(t *testing.T) {
	directory := os.Getenv("VEX_IT_ACTION_SOURCE")
	if directory == "" {
		t.Skip("requires the independent vex-action source directory")
	}
	controller, err := os.ReadFile("testdata/vex-build.json")
	if err != nil {
		t.Fatal(err)
	}
	action, err := os.ReadFile(filepath.Join(directory, "testdata", "vex-build.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(controller, action) {
		t.Fatal("controller and vex-action bundle vectors differ")
	}
}
