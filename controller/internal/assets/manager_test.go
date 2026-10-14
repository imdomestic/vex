package assets

import (
	"errors"
	"testing"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

func TestCompatibilityChecksVersionLoaderAndNamespaces(t *testing.T) {
	version := "26.3"
	build := model.Build{Minecraft: &version, Loader: "fabric", ModIDs: []string{"chorus", "fabric-api"}}
	revision := model.AssetRevision{AssetMetadata: model.AssetMetadata{Minecraft: version, Requires: model.Requirements{Loaders: []string{"fabric"}, ChorusMod: true, Namespaces: []string{}}}}
	if err := ValidateCompatibility(revision, build); err != nil { t.Fatal(err) }
	for _, kind := range []string{"minecraft", "loader", "namespace", "chorus"} {
		candidate := revision
		candidateBuild := build
		switch kind {
		case "minecraft": candidate.Minecraft = "26.2"
		case "loader": candidate.Requires.Loaders = []string{"neoforge"}
		case "namespace": candidate.Requires.Namespaces = []string{"unavailable"}
		case "chorus": candidateBuild.ModIDs = []string{"fabric-api"}
		}
		var failure *model.Error
		if err := ValidateCompatibility(candidate, candidateBuild); !errors.As(err, &failure) || failure.Code != "build_mismatch" { t.Fatalf("%s accepted: %v", kind, err) }
	}
}

func TestPublishedIdentifierValidation(t *testing.T) {
	for _, id := range []string{"plaza", "高塔 资源包", "a.b-c_1"} { if !validPackID(id) { t.Fatalf("valid pack ID rejected: %q", id) } }
	for _, id := range []string{"", ".", "..", "a/b", "a\\b", "a:b", "a\nb"} { if validPackID(id) { t.Fatalf("unsafe pack ID accepted: %q", id) } }
	for _, rev := range []string{"1", "2", "123456"} { if !validRevision(rev) { t.Fatalf("valid revision rejected: %q", rev) } }
	for _, rev := range []string{"", "0", "01", "-1", "+1", "1.0", "../1"} { if validRevision(rev) { t.Fatalf("invalid revision accepted: %q", rev) } }
}
