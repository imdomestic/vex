package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
)

func TestDeviceAPIRejectsUnavailableProviderAndUnknownHandles(t *testing.T) {
	service, server, _ := loginEnvironment(t)
	assertFailure := func(path string, body any, status int, code string) {
		t.Helper()
		actual, data, _ := loginRequest(t, server, "POST", path, "", body)
		var failure struct {
			Error model.Error `json:"error"`
		}
		if err := json.Unmarshal(data, &failure); err != nil || actual != status || failure.Error.Code != code {
			t.Fatalf("device endpoint returned HTTP %d code=%s, expected %d %s", actual, failure.Error.Code, status, code)
		}
	}
	assertFailure("/v2/auth/device", map[string]string{}, 503, "provider_unavailable")
	assertFailure("/v2/auth/device/poll", map[string]string{"handle": strings.Repeat("a", 43), "client": "real HTTP test"}, 503, "provider_unavailable")
	device, err := auth.NewDeviceFlow(t.Context(), service.Auth, "Iv23li5DpGaXG30bErGB")
	if err != nil {
		t.Fatal(err)
	}
	service.Device = device
	assertFailure("/v2/auth/device", map[string]string{"provider": "github"}, 400, "invalid_request")
	assertFailure("/v2/auth/device/poll", map[string]string{"id": strings.Repeat("a", 43), "client": "real HTTP test"}, 400, "invalid_request")
	assertFailure("/v2/auth/device/poll", map[string]string{"handle": "invalid", "client": "real HTTP test"}, 400, "invalid_request")
	assertFailure("/v2/auth/device/poll", map[string]string{"handle": strings.Repeat("a", 43), "client": ""}, 400, "invalid_request")
	assertFailure("/v2/auth/device/poll", map[string]string{"handle": strings.Repeat("a", 43), "client": "real HTTP test"}, 400, "expired_token")
}
