//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestDeviceFlowUsesInstalledGitHubApp(t *testing.T) {
	clientID := os.Getenv("VEX_IT_GITHUB_CLIENT_ID")
	if clientID == "" {
		t.Skip("VEX_IT_GITHUB_CLIENT_ID is required")
	}
	service, server, db := loginEnvironment(t)
	device, err := auth.NewDeviceFlow(t.Context(), service.Auth, clientID)
	if err != nil {
		t.Fatal(err)
	}
	service.Device = device
	status, data, _ := loginRequest(t, server, "POST", "/v2/auth/device", "", map[string]string{})
	var challenge auth.DeviceChallenge
	if status != 200 || json.Unmarshal(data, &challenge) != nil {
		t.Fatalf("real GitHub App device request returned HTTP %d", status)
	}
	if len(challenge.Handle) != 43 || challenge.UserCode == "" || challenge.VerificationURI != "https://github.com/login/device" || challenge.Interval < 5 || !challenge.ExpiresAt.After(time.Now()) {
		t.Fatal("real device challenge lacks required public fields")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil || len(fields) != 5 || fields["device_code"] != nil || fields["deviceCode"] != nil {
		t.Fatal("device endpoint exposed provider internals")
	}
	request := map[string]string{"handle": challenge.Handle, "client": "real GitHub App integration"}
	status, data, headers := loginRequest(t, server, "POST", "/v2/auth/device/poll", "", request)
	var pending struct {
		Status   string `json:"status"`
		Interval int64  `json:"interval"`
	}
	if status != 202 || json.Unmarshal(data, &pending) != nil || pending.Status != "pending" || pending.Interval != challenge.Interval || headers.Get("Retry-After") != strconv.FormatInt(challenge.Interval, 10) {
		t.Fatalf("unapproved real device challenge returned HTTP %d", status)
	}
	status, data, headers = loginRequest(t, server, "POST", "/v2/auth/device/poll", "", request)
	var failure struct {
		Error model.Error `json:"error"`
	}
	if status != 429 || json.Unmarshal(data, &failure) != nil || failure.Error.Code != "rate_limited" || headers.Get("Retry-After") == "" {
		t.Fatal("device polling interval was not enforced")
	}
	platform, err := db.Platform()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"auth_session", "auth_session_token"} {
		values, err := platform.List(t.Context(), kind)
		if err != nil || len(values) != 0 {
			t.Fatal("unapproved device request created a session", err)
		}
	}
}

func TestDeviceFlowSurvivesRestartAndCancellationDeletesRecord(t *testing.T) {
	clientID := os.Getenv("VEX_IT_GITHUB_CLIENT_ID")
	if clientID == "" {
		t.Skip("VEX_IT_GITHUB_CLIENT_ID is required")
	}
	directory := t.TempDir()
	service, server, db := loginEnvironmentIn(t, directory)
	first, stop := context.WithCancel(t.Context())
	device, err := auth.NewDeviceFlow(first, service.Auth, clientID)
	if err != nil {
		t.Fatal(err)
	}
	service.Device = device
	status, data, _ := loginRequest(t, server, "POST", "/v2/auth/device", "", map[string]string{})
	var challenge auth.DeviceChallenge
	if status != 200 || json.Unmarshal(data, &challenge) != nil {
		t.Fatalf("real GitHub App device request returned HTTP %d", status)
	}
	request := map[string]string{"handle": challenge.Handle, "client": "real GitHub App restart"}
	if status, _, _ = loginRequest(t, server, "POST", "/v2/auth/device/poll", "", request); status != 202 {
		t.Fatalf("pending authorization returned HTTP %d", status)
	}
	platform, err := db.Platform()
	if err != nil {
		t.Fatal(err)
	}
	records, err := platform.List(t.Context(), "device_attempt")
	if err != nil || len(records) != 1 {
		t.Fatal("pending authorization was not stored", err)
	}
	var stored map[string]any
	if err = json.Unmarshal(records[0], &stored); err != nil || stored["deviceCode"] == "" || stored["client"] != "real GitHub App restart" || stored["interval"] != float64(challenge.Interval) || stored["handle"] == challenge.Handle || bytes.Contains(records[0], []byte(challenge.Handle)) {
		t.Fatal("stored authorization lacks its fields or exposes the client handle", err)
	}
	// 停止原服务的后台查询，重新打开同一个数据库，相当于 vexd 重启。
	stop()
	reopened, err := store.Open(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	identity, err := auth.NewWithSessions(t.Context(), service.Global, reopened)
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(service.Global, map[string]*Server{}, identity, nil)
	restarted.Device, err = auth.NewDeviceFlow(t.Context(), identity, clientID)
	if err != nil {
		t.Fatal(err)
	}
	after := httptest.NewServer(restarted.Handler())
	t.Cleanup(after.Close)
	status, data, _ = loginRequest(t, after, "POST", "/v2/auth/device/poll", "", request)
	var pending struct {
		Status string `json:"status"`
	}
	if status != 202 || json.Unmarshal(data, &pending) != nil || pending.Status != "pending" {
		t.Fatalf("authorization was not pending after restart: HTTP %d", status)
	}
	if status, _, _ = loginRequest(t, after, "POST", "/v2/auth/device/cancel", "", map[string]string{"handle": challenge.Handle}); status != 204 {
		t.Fatalf("cancellation returned HTTP %d", status)
	}
	if records, err = platform.List(t.Context(), "device_attempt"); err != nil || len(records) != 0 {
		t.Fatal("cancelled authorization record remains", err)
	}
	status, data, _ = loginRequest(t, after, "POST", "/v2/auth/device/poll", "", request)
	var failure struct {
		Error model.Error `json:"error"`
	}
	if status != 400 || json.Unmarshal(data, &failure) != nil || failure.Error.Code != "expired_token" {
		t.Fatalf("cancelled authorization still answered: HTTP %d", status)
	}
}
