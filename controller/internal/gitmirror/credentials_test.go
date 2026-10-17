package gitmirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitConfigurationIsProjectLocal(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git(t, root, "init", "--initial-branch=main", source)
	sha := commit(t, source, "readme", "project credentials isolation")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file:///unavailable/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", source)
	t.Setenv("GIT_ASKPASS", filepath.Join(root, "unavailable-askpass"))
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(root, "unavailable-agent"))
	mirror, err := New(source, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = mirror.VerifyCommit(t.Context(), sha); err != nil {
		t.Fatal(err)
	}
	value, err := mirror.git(t.Context(), "config", "--get-all", "credential.helper")
	if err != nil || strings.TrimSpace(value) != "" {
		t.Fatal("inherited credential helper was not cleared", err)
	}
	for _, entry := range cleanEnvironment() {
		if strings.HasPrefix(entry, "SSH_AUTH_SOCK=") || strings.HasPrefix(entry, "GIT_ASKPASS=") || strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") {
			t.Fatal("inherited authentication environment retained")
		}
	}
}

func TestGitCredentialsRejectAmbiguousConfiguration(t *testing.T) {
	root := t.TempDir()
	for _, credentials := range []Credentials{
		{SSHIdentityFile: filepath.Join(root, "key")},
		{SSHIdentityFile: filepath.Join(root, "key"), KnownHostsFile: filepath.Join(root, "hosts"), HTTPSTokenFile: filepath.Join(root, "token")},
		{HTTPSTokenFile: filepath.Join(root, "token")},
		{SSHIdentityFile: "relative", KnownHostsFile: filepath.Join(root, "hosts")},
	} {
		if _, err := NewWithCredentials("https://example.invalid/project.git", filepath.Join(root, "mirror.git"), credentials); err == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "mirror.git")); !os.IsNotExist(err) {
		t.Fatal("configuration validation created a repository")
	}
}
