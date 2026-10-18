package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
)

func TestDeviceChallengeValidation(t *testing.T) {
	now := time.Now()
	challenge := auth.DeviceChallenge{Handle: "device-input", UserCode: "ABCD-EFGH", VerificationURI: "https://github.com/login/device", ExpiresAt: now.Add(time.Minute), Interval: 5}
	if value, err := deviceVerification(challenge, now); err != nil || value != challenge.VerificationURI {
		t.Fatal("valid device challenge rejected")
	}
	for _, address := range []string{"http://github.com/login/device", "https://user@github.com/login/device", "https://github.com/login/device#credential", ""} {
		invalid := challenge
		invalid.VerificationURI = address
		if _, err := deviceVerification(invalid, now); err == nil {
			t.Fatal("invalid verification address accepted")
		}
	}
	for _, interval := range []int64{0, 61} {
		invalid := challenge
		invalid.Interval = interval
		if _, err := deviceVerification(invalid, now); err == nil {
			t.Fatal("invalid polling interval accepted")
		}
	}
	if _, err := deviceVerification(challenge, challenge.ExpiresAt); err == nil {
		t.Fatal("expired authorization accepted")
	}
	if interval, err := devicePending(strings.NewReader(`{"status":"pending","interval":5}`)); err != nil || interval != 5*time.Second {
		t.Fatal("pending response rejected")
	}
	for _, input := range []string{`{"status":"ready","interval":5}`, `{"status":"pending","interval":0}`, `{"status":"pending","interval":61}`, `{}`} {
		if _, err := devicePending(strings.NewReader(input)); err == nil {
			t.Fatal("invalid polling response accepted")
		}
	}
}

func TestDeviceLoginUnavailableWithRealHandler(t *testing.T) {
	cleanClientEnvironment(t)
	_, server, _ := cliLoginServer(t, "Device test")
	file := filepath.Join(t.TempDir(), "client.toml")
	var output bytes.Buffer
	err := runWithInput(t.Context(), []string{"--config", file, "login", server.URL, "--github"}, strings.NewReader(""), &output)
	if err == nil || !strings.Contains(err.Error(), "does not offer GitHub") {
		t.Fatal("disabled provider was not rejected from real metadata")
	}
	c, err := newClient(config.Client{URL: server.URL, Timeout: "10s"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.githubLogin(t.Context(), "vex test", &output); err == nil || !strings.Contains(err.Error(), "provider_unavailable") {
		t.Fatal("real device endpoint did not return provider_unavailable")
	}
	for _, extra := range [][]string{{"--token-stdin"}, {"--github"}} {
		args := append([]string{"--config", file, "whoami"}, extra...)
		if err = runWithInput(t.Context(), args, strings.NewReader(""), &output); err == nil {
			t.Fatal("login flag accepted by whoami")
		}
	}
}
