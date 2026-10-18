package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const exampleManifest = `schema = 1
[minecraft]
version = "26.3"
java = 25
[loaders.fabric]
loader_version = "0.19.5"
agent = true
[[loaders.fabric.files]]
glob = "build/libs/*.jar"
exclude = ["*-sources.jar"]
side = "both"
`

func artifact(t *testing.T, root, name string) string {
	t.Helper()
	filename := filepath.Join(root, "build", "libs", name)
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("fabric.mod.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(entry, `{"schemaVersion":1,"id":"bundle_test","version":"1.0.0","environment":"*"}`); err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(archive.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestActualArchiveCollectionAndPathBoundaries(t *testing.T) {
	doc, err := parseManifest([]byte(exampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	file := artifact(t, root, "example.jar")
	artifact(t, root, "example-sources.jar")
	bundle, err := collect(t.Context(), root, doc, ".work/output")
	if err != nil {
		t.Fatal(err)
	}
	files := bundle.Loaders["fabric"].Files
	if len(files) != 1 || files[0].Origin != "build" || files[0].Name != "example.jar" || files[0].URL != "" {
		t.Fatalf("unexpected collected files: %+v", files)
	}
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(root, ".work", "output", files[0].Path))
	if err != nil || !bytes.Equal(original, copied) {
		t.Fatal("artifact bytes changed", err)
	}
	if _, err = collect(t.Context(), root, doc, ".work/output"); err == nil {
		t.Fatal("existing output accepted")
	}
	if _, err = collect(t.Context(), root, doc, "../outside"); err == nil {
		t.Fatal("escaping output accepted")
	}
	if err = os.Symlink(file, filepath.Join(root, "build", "libs", "linked.jar")); err != nil {
		t.Fatal(err)
	}
	if _, err = collect(t.Context(), root, doc, ".work/linked"); err == nil {
		t.Fatal("symbolic link artifact accepted")
	}
}

func TestManifestRequiresCompleteDependencyDigests(t *testing.T) {
	dependency := "\n[[loaders.fabric.deps]]\nurl='https://example.com/file.jar'\nside='both'\nsha512='" + strings.Repeat("a", 128) + "'\n"
	if _, err := parseManifest([]byte(exampleManifest + dependency)); err == nil {
		t.Fatal("dependency without SHA1 accepted")
	}
	if _, err := parseManifest([]byte(exampleManifest + dependency + "sha1='" + strings.Repeat("a", 40) + "'\n")); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(exampleManifest, "schema = 1", "schema = 2", 1),
		strings.Replace(exampleManifest, "build/libs/*.jar", "../outside/*.jar", 1),
		strings.Replace(exampleManifest, "side = \"both\"", "side = \"invalid\"", 1),
		"unknown = true\n" + exampleManifest,
	} {
		if _, err := parseManifest([]byte(invalid)); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
}

func TestSharedBundleVector(t *testing.T) {
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
	if vector.Schema != 1 || vector.Minecraft != "26.3" || vector.Java != 25 || vector.Loaders["fabric"].LoaderVersion != "0.19.5" || len(vector.Loaders["fabric"].Files) != 1 {
		t.Fatal("bundle vector schema changed")
	}
	file := vector.Loaders["fabric"].Files[0]
	if file.Origin != "build" || file.Size != 3453 || !digest(file.SHA1, 40) || !digest(file.SHA256, 64) || !digest(file.SHA512, 128) {
		t.Fatal("bundle vector file changed")
	}
}

func TestActualProductionJarMatchesSharedVector(t *testing.T) {
	filename := os.Getenv("VEX_IT_VECTOR_JAR")
	if filename == "" {
		t.Skip("requires the actual example production artifact")
	}
	data, err := os.ReadFile("testdata/vex-build.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector Bundle
	if err = json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := copyDescribed(input, filepath.Join(t.TempDir(), "verified.jar"))
	if err = errors.Join(err, input.Close()); err != nil {
		t.Fatal(err)
	}
	expected := vector.Loaders["fabric"].Files[0]
	actual.Name, actual.Path, actual.Side, actual.Origin = expected.Name, expected.Path, expected.Side, expected.Origin
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual artifact differs from shared vector: %+v", actual)
	}
}

func TestCollectionRejectsActualOversizedFile(t *testing.T) {
	doc, err := parseManifest([]byte(exampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	filename := artifact(t, root, "large.jar")
	if err = os.Truncate(filename, maxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err = collect(t.Context(), root, doc, ".work/oversized"); err == nil {
		t.Fatal("oversized regular file accepted")
	}
}
