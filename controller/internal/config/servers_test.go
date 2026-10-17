package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestServerSelectionAndPersistentRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.toml")
	if err := SaveClientServer(path, "one", ClientServer{URL: "https://one.example", Name: "One", Project: "example"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveClientServer(path, "two", ClientServer{URL: "https://two.example", Name: "Two"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := LoadClient(Options{Args: []string{"--config", path, "--server", "two", "--project", "command"}, Environ: []string{"VEX_DEFAULT_SERVER=one"}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := resolved.Config.Selected()
	if err != nil || selected.URL != "https://two.example" || selected.Project != "command" || selected.Path != path {
		t.Fatal("server selection did not respect configuration layers")
	}
	resolved.Config.Server, resolved.Config.DefaultServer = "", ""
	if _, err = resolved.Config.Selected(); err == nil {
		t.Fatal("ambiguous server selection accepted")
	}
	if err = SaveClientServer(path, "one", ClientServer{URL: "https://different.example"}); err == nil {
		t.Fatal("server registration silently changed its URL")
	}
}

func TestKubernetesInstanceNamespaceValidation(t *testing.T) {
	cfg := Defaults()
	for _, prefix := range []string{"", "vex", "Upper-", "-bad-", strings.Repeat("a", 40) + "-"} {
		cfg.Runtime.Kubernetes.NamespacePrefix = prefix
		if err := cfg.Validate(); err == nil {
			t.Fatal("invalid namespace prefix accepted")
		}
	}
	cfg = Defaults()
	cfg.Runtime.Kubernetes.NamespacePrefix = "another-instance-"
	cfg.Runtime.Kubernetes.SystemNamespace = "another-system"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Projects["example"] = Project{}
	cfg.Runtime.Kubernetes.SystemNamespace = "another-instance-example"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "system_namespace") {
		t.Fatal("project namespace conflicts with system namespace")
	}
}
