package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDocumentedRoutesMatchRouter(t *testing.T) {
	filename := os.Getenv("VEX_API_DOCUMENT")
	if filename == "" {
		filename = filepath.Join("..", "..", "..", "docs", "api.md")
	}
	document, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "```json vex-routes\n"
	if bytes.Count(document, []byte(marker)) != 1 {
		t.Fatal("docs/api.md 必须包含一个 vex-routes JSON 区块")
	}
	var entries [][]byte
	seen := map[string]bool{}
	for _, route := range Routes() {
		key := route.Method + " " + route.Path
		if seen[key] {
			t.Fatalf("路由重复：%s", key)
		}
		seen[key] = true
		entry, err := json.Marshal([3]string{route.Method, route.Path, route.Permission})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, append([]byte("  "), entry...))
	}
	var expected bytes.Buffer
	expected.WriteString(marker + "[\n")
	expected.Write(bytes.Join(entries, []byte(",\n")))
	expected.WriteString("\n]\n```\n")
	if bytes.Count(document, expected.Bytes()) != 1 {
		t.Fatalf("docs/api.md 的路由契约与 Routes() 不一致；期望区块：\n%s", expected.String())
	}
}
