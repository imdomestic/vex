package config

import (
	"strings"
	"testing"
)

func TestListenerConfigurationAcrossLayers(t *testing.T) {
	document := `[server]
tls_cert_file = "/certificates/fullchain.pem"
tls_key_file = "/certificates/key.pem"
public_base_url = "https://vex.example.invalid:8790"
internal_base_url = "http://10.0.0.1:8790"
listeners = [
  {address="100.64.0.1:8790", tls=true, scope="public"},
  {address="10.0.0.1:8790", tls=false, scope="internal"}
]
`
	file := writeConfig(t, document)
	resolved, err := LoadDaemon(Options{Args: []string{"--config", file}, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	endpoints := resolved.Config.Server.Endpoints()
	if len(resolved.Config.Server.Listen) != 0 || len(endpoints) != 2 || !endpoints[0].TLS || endpoints[1].TLS || endpoints[1].Scope != "internal" {
		t.Fatal("listener configuration did not preserve separate protocols and scopes")
	}
	for _, options := range []Options{
		{Args: []string{"--config", file, "--server.listen=127.0.0.1:8792"}, Environ: []string{}},
		{Args: []string{"--config", file}, Environ: []string{"VEX_SERVER__LISTEN=127.0.0.1:8792"}},
	} {
		if _, err := LoadDaemon(options); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatal("ambiguous listener settings were accepted")
		}
	}
	_, err = LoadDaemon(Options{Args: []string{"--config", file, `--server.listeners=[{address="127.0.0.1:8791",tls=true,scope="public"}]`}, Environ: []string{}})
	if err != nil {
		t.Fatal("command line listener override:", err)
	}
}

func TestListenerConfigurationRejectsInvalidTLSAndScope(t *testing.T) {
	for _, document := range []string{
		`[server]
tls_cert_file="/cert.pem"`,
		`[server]
tls_key_file="/key.pem"`,
		`[server]
tls_cert_file="relative.pem"
tls_key_file="/key.pem"`,
		`[server]
listeners=[{address="127.0.0.1:8790",tls=true,scope="public"}]`,
		`[server]
listeners=[{address="127.0.0.1:8790",tls=false,scope="unknown"}]`,
		`[server]
listeners=[{address="127.0.0.1:8790",scope="public"},{address="127.0.0.1:8790",scope="internal"}]`,
		`[server]
listeners=[{address="invalid",scope="public"}]`,
	} {
		_, err := LoadDaemon(Options{Args: []string{"--config", writeConfig(t, document)}, Environ: []string{}})
		if err == nil {
			t.Fatal("invalid TLS/listener configuration accepted")
		}
	}
}
