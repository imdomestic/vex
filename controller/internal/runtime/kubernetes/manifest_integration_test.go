//go:build integration

package kubernetes_test

import (
	"testing"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
)

func loadActualBuildManifest(t *testing.T, cfg config.ProjectConfig, mirrorPath string, value *model.Build) {
	t.Helper()
	mirror, err := gitmirror.New(cfg.Git.Remote, mirrorPath)
	if err != nil {
		t.Fatal(err)
	}
	document, _, err := manifest.ReadCommit(t.Context(), mirror, value.SHA, cfg.ManifestFallback)
	if err != nil {
		t.Fatal("actual build fixture requires its commit manifest or configured manifest fallback", err)
	}
	loader, exists := document.Loaders[value.Loader]
	if !exists || value.Minecraft == nil || value.Java == nil || value.LoaderVersion == nil || *value.Minecraft != document.Minecraft.Version || *value.Java != document.Minecraft.Java || *value.LoaderVersion != loader.LoaderVersion {
		t.Fatal("actual build runtime identity differs from its authoritative manifest")
	}
	value.WorldExtraRootEntries = append([]string{}, document.World.ExtraRootEntries...)
	value.RoomMemory = document.Rooms.Memory
	value.ClientFingerprint, err = build.ClientFingerprint(*value)
	if err != nil {
		t.Fatal("actual build client files cannot produce a verified fingerprint", err)
	}
}
