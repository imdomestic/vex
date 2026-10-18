//go:build integration

package api

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
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
