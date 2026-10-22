package githubapp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func setupServer(t *testing.T, directory string) (*Setup, *httptest.Server) {
	t.Helper()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	setup, err := NewSetup("imdomestic", "Vex integration", directory, "http://"+server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = setup.Handler()
	server.Start()
	t.Cleanup(server.Close)
	return setup, server
}

func htmlInput(t *testing.T, body io.Reader, name string) string {
	t.Helper()
	document, err := html.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "input" {
			attributes := map[string]string{}
			for _, attribute := range node.Attr {
				attributes[attribute.Key] = attribute.Val
			}
			if attributes["name"] == name {
				value = attributes["value"]
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return value
}

func TestSetupRegistrationIsSingleUseAndManifestIsScoped(t *testing.T) {
	directory := t.TempDir()
	setup, server := setupServer(t, directory)
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := htmlInput(t, response.Body, "state")
	response.Body.Close()
	if state == "" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("setup page must have a state and disable caching")
	}
	response, err = server.Client().PostForm(server.URL+"/begin", url.Values{"state": {state}})
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Public      bool              `json:"public"`
		Permissions map[string]string `json:"default_permissions"`
		Redirect    string            `json:"redirect_url"`
		Hook        struct {
			Active bool `json:"active"`
		} `json:"hook_attributes"`
	}
	encoded := htmlInput(t, response.Body, "manifest")
	response.Body.Close()
	if err = json.Unmarshal([]byte(encoded), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Public || manifest.Hook.Active || manifest.Redirect != server.URL+"/callback" || len(manifest.Permissions) != 5 || manifest.Permissions["pull_requests"] != "write" || manifest.Permissions["contents"] != "read" {
		t.Fatal("unexpected App permissions or callback")
	}
	response, err = server.Client().PostForm(server.URL+"/begin", url.Values{"state": {state}})
	if err != nil {
		t.Fatal(err)
	}
	if htmlInput(t, response.Body, "manifest") != "" {
		t.Fatal("a repeated request must not start another registration")
	}
	response.Body.Close()
	if _, err = NewSetup("imdomestic", "Vex integration", directory, setup.base); err == nil {
		t.Fatal("an unfinished registration must require inspection before restart")
	}
	info, err := os.Stat(filepath.Join(directory, "registration-started"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("registration marker must be private")
	}
}

func TestSetupResumesCompletedRegistration(t *testing.T) {
	directory := t.TempDir()
	data, err := json.Marshal(AppRegistration{ID: 5366762, Slug: "vex-imdomestic", ClientID: "Iv23li5DpGaXG30bErGB"})
	if err != nil {
		t.Fatal(err)
	}
	if err = privateFile(filepath.Join(directory, "app.json"), data); err != nil {
		t.Fatal(err)
	}
	_, server := setupServer(t, directory)
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "/vex-imdomestic/installations/new") || !strings.Contains(string(body), "Enable Device Flow") || strings.Contains(string(body), `action="/begin"`) {
		t.Fatal("completed setup must retain settings and installation links")
	}
}

func TestSetupRecognizesEarlierRegistrationScriptFiles(t *testing.T) {
	private := func() string {
		t.Helper()
		directory := t.TempDir()
		if err := os.Chmod(directory, 0700); err != nil {
			t.Fatal(err)
		}
		return directory
	}
	directory := private()
	data, err := json.Marshal(map[string]any{"id": 5366762, "slug": "vex-imdomestic", "client_id": "Iv23li5DpGaXG30bErGB", "html_url": "https://github.com/apps/vex-imdomestic"})
	if err != nil {
		t.Fatal(err)
	}
	if err = privateFile(filepath.Join(directory, "github-app.json"), data); err != nil {
		t.Fatal(err)
	}
	setup, err := NewSetup("imdomestic", "Vex", directory, "http://127.0.0.1:18793")
	if err != nil || setup.app == nil || setup.app.Slug != "vex-imdomestic" || setup.app.ClientID != "Iv23li5DpGaXG30bErGB" {
		t.Fatal("earlier registration metadata was not recognized", err)
	}
	for _, name := range []string{"github-app.pem", "github-webhook.key"} {
		partial := private()
		if err = privateFile(filepath.Join(partial, name), []byte("registration file")); err != nil {
			t.Fatal(err)
		}
		if _, err = NewSetup("imdomestic", "Vex", partial, "http://127.0.0.1:18793"); err == nil {
			t.Fatalf("a directory containing %s accepted a new registration", name)
		}
	}
	invalid := private()
	if err = privateFile(filepath.Join(invalid, "github-app.json"), []byte(`{"id":0}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = NewSetup("imdomestic", "Vex", invalid, "http://127.0.0.1:18793"); err == nil {
		t.Fatal("invalid earlier registration metadata accepted")
	}
}

func TestSetupRejectsInvalidStateAndHost(t *testing.T) {
	_, server := setupServer(t, t.TempDir())
	response, err := server.Client().PostForm(server.URL+"/begin", url.Values{"state": {"invalid"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("invalid state was accepted")
	}
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "foreign.example"
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("foreign Host was accepted")
	}
}
