//go:build integration

package testfixture

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
)

func Manifest(t *testing.T) (manifest.Document, string) {
	t.Helper()
	buildPath, mirrorPath := os.Getenv("VEX_IT_BUILD"), os.Getenv("VEX_IT_MIRROR")
	if buildPath == "" || mirrorPath == "" {
		t.Fatal("world integration requires an actual ready build manifest and Git mirror")
	}
	data, err := os.ReadFile(buildPath)
	if err != nil {
		t.Fatal(err)
	}
	var value model.Build
	if err = json.Unmarshal(data, &value); err != nil || value.Status != "ready" {
		t.Fatal("world integration build is not an actual ready build", err)
	}
	mirror, err := gitmirror.New(mirrorPath, mirrorPath)
	if err != nil {
		t.Fatal(err)
	}
	document, _, err := manifest.ReadCommit(t.Context(), mirror, value.SHA, os.Getenv("VEX_IT_MANIFEST_FALLBACK"))
	if err != nil {
		t.Fatal("cannot read the actual build's authoritative manifest", err)
	}
	if value.Minecraft == nil || value.Java == nil || value.LoaderVersion == nil || *value.Minecraft != document.Minecraft.Version || *value.Java != document.Minecraft.Java || *value.LoaderVersion != document.Loaders[value.Loader].LoaderVersion {
		t.Fatal("actual build identity differs from its authoritative manifest")
	}
	return document, value.SHA
}
