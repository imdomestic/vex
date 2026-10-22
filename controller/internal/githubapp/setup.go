package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v88/github"
)

type AppRegistration struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	ClientID string `json:"client_id"`
}

type Setup struct {
	mu        sync.Mutex
	owner     string
	name      string
	directory string
	base      string
	state     string
	started   bool
	app       *AppRegistration
	failure   chan error
}

func NewSetup(owner, name, directory, base string) (*Setup, error) {
	if !loginPattern.MatchString(owner) || strings.TrimSpace(name) == "" || len(name) > 100 || strings.ContainsAny(name, "\x00\r\n") {
		return nil, fmt.Errorf("create-app requires a valid owner and app name")
	}
	address, err := url.Parse(base)
	if err != nil || address.Scheme != "http" || address.Hostname() != "127.0.0.1" || address.Port() == "" || address.Path != "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return nil, fmt.Errorf("App setup requires a loopback callback address")
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("App output directory must be a private directory (0700)")
	}
	setup := &Setup{owner: owner, name: name, directory: directory, base: base, state: rand.Text(), failure: make(chan error, 1)}
	// github-app.json、github-app.pem 与 github-webhook.key 是早期注册脚本的文件名，同样表示该目录已有注册。
	for _, metadata := range []string{"app.json", "github-app.json"} {
		data, err := os.ReadFile(filepath.Join(directory, metadata))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var registration AppRegistration
		if err = json.Unmarshal(data, &registration); err != nil || registration.ID <= 0 || registration.Slug == "" || registration.ClientID == "" {
			return nil, fmt.Errorf("existing %s is invalid", metadata)
		}
		setup.app = &registration
		return setup, nil
	}
	for _, path := range []string{"registration-started", "private-key.pem", "client-secret", "github-app.pem", "github-webhook.key"} {
		if _, err := os.Lstat(filepath.Join(directory, path)); err == nil {
			return nil, fmt.Errorf("App registration already started in this directory; inspect the existing GitHub App before starting another registration")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return setup, nil
}

func (s *Setup) Errors() <-chan error { return s.failure }

func (s *Setup) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("POST /begin", s.begin)
	mux.HandleFunc("GET /callback", s.callback)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		address := net.ParseIP(host)
		if err != nil || address == nil || !address.IsLoopback() || "http://"+r.Host != s.base {
			http.Error(w, "Loopback access required", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://github.com; frame-ancestors 'none'; base-uri 'none'")
		mux.ServeHTTP(w, r)
	})
}

var setupPage = template.Must(template.New("setup").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Vex GitHub App</title><style>body{font:18px system-ui;max-width:760px;margin:4rem auto;padding:0 1.5rem;line-height:1.7}button,a{font:inherit}button{padding:.6rem 1rem}li{margin:1rem 0}code{overflow-wrap:anywhere}</style><h1>Vex GitHub App</h1>
{{if .App}}<p>App <strong>{{.App.Slug}}</strong> 已创建，凭据已保存到 <code>{{.Directory}}</code>。</p><ol><li><a href="https://github.com/organizations/{{.Owner}}/settings/apps/{{.App.Slug}}" target="_blank" rel="noreferrer">打开 App 设置</a>，启用 Enable Device Flow 并保存。</li><li><a href="https://github.com/apps/{{.App.Slug}}/installations/new" target="_blank" rel="noreferrer">安装 App</a>，选择需要接入的仓库。</li><li>部署凭据后，运行 <code>vexd github check</code> 检查权限。</li></ol><p>可以保存本页地址，在设置与安装页面之间切换。完成后在终端按 Ctrl+C 关闭此页面服务。</p>
{{else if .Started}}<p>注册流程已开始。请完成 GitHub 页面中的创建操作，GitHub 会返回此页面。</p><p><a href="https://github.com/organizations/{{.Owner}}/settings/apps" target="_blank" rel="noreferrer">查看组织现有的 App</a></p>
{{else}}<p>为 {{.Owner}} 创建 {{.Name}}，用于读取 CI 构建、发布预览状态与 GitHub 登录。</p><p>权限：Actions 和 Contents 读取，Checks 和 Pull requests 写入，Metadata 读取。Webhook 关闭。</p><form action="/begin" method="post"><input type="hidden" name="state" value="{{.State}}"><button type="submit">前往 GitHub 创建 App</button></form>{{end}}</html>`))

func (s *Setup) page(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := setupPage.Execute(w, struct {
		Owner, Name, Directory, State string
		Started                       bool
		App                           *AppRegistration
	}{s.owner, s.name, s.directory, s.state, s.started, s.app}); err != nil {
		s.fail(err)
	}
}

var registrationPage = template.Must(template.New("registration").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>创建 GitHub App</title><h1>创建 GitHub App</h1><p>GitHub 创建完成后会返回 Vex 设置页面。</p><form action="{{.Action}}" method="post"><input type="hidden" name="manifest" value="{{.Manifest}}"><button type="submit">打开 GitHub 注册页面</button></form><p><a href="/">返回设置进度</a></p></html>`))

func (s *Setup) begin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil || subtle.ConstantTimeCompare([]byte(r.Form.Get("state")), []byte(s.state)) != 1 {
		http.Error(w, "Invalid setup request", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.app != nil || s.started {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := privateFile(filepath.Join(s.directory, "registration-started"), []byte(time.Now().UTC().Format(time.RFC3339)+"\n")); err != nil {
		http.Error(w, "Cannot record registration", http.StatusInternalServerError)
		s.fail(err)
		return
	}
	s.started = true
	manifest, err := json.Marshal(map[string]any{
		"name": s.name, "url": "https://github.com/" + s.owner,
		"redirect_url": s.base + "/callback", "public": false,
		"hook_attributes": map[string]any{"active": false}, "default_events": []string{},
		"default_permissions": map[string]string{"actions": "read", "checks": "write", "contents": "read", "metadata": "read", "pull_requests": "write"},
	})
	if err != nil {
		s.fail(err)
		return
	}
	action := "https://github.com/organizations/" + url.PathEscape(s.owner) + "/settings/apps/new?state=" + url.QueryEscape(s.state)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err = registrationPage.Execute(w, struct{ Action, Manifest string }{action, string(manifest)}); err != nil {
		s.fail(err)
	}
}

func (s *Setup) callback(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(s.state)) != 1 {
		http.Error(w, "Invalid callback state", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.app != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	code := r.URL.Query().Get("code")
	if !s.started || len(code) < 10 || len(code) > 256 || strings.ContainsAny(code, "/\\?#\r\n\x00") {
		http.Error(w, "Invalid callback code", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	registration, err := s.exchange(ctx, code)
	if err != nil {
		http.Error(w, "App registration could not be saved. See the terminal for the next action.", http.StatusInternalServerError)
		s.fail(err)
		return
	}
	s.app = &registration
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Setup) exchange(ctx context.Context, code string) (AppRegistration, error) {
	client, err := github.NewClient()
	if err != nil {
		return AppRegistration{}, err
	}
	request, err := client.NewRequest(ctx, http.MethodPost, "app-manifests/"+url.PathEscape(code)+"/conversions", nil)
	if err != nil {
		return AppRegistration{}, fmt.Errorf("cannot prepare App registration")
	}
	var result struct {
		AppRegistration
		PEM          string `json:"pem"`
		ClientSecret string `json:"client_secret"`
	}
	if _, err = client.Do(request, &result); err != nil {
		return AppRegistration{}, SafeError("convert App manifest", err)
	}
	if result.ID <= 0 || result.Slug == "" || result.ClientID == "" || result.PEM == "" || result.ClientSecret == "" {
		return AppRegistration{}, fmt.Errorf("GitHub returned incomplete App credentials")
	}
	metadata, err := json.MarshalIndent(result.AppRegistration, "", "  ")
	if err != nil {
		return AppRegistration{}, err
	}
	for _, file := range []struct{ name, value string }{
		{"private-key.pem", result.PEM}, {"client-secret", result.ClientSecret}, {"app.json", string(metadata) + "\n"},
	} {
		if err = privateFile(filepath.Join(s.directory, file.name), []byte(file.value)); err != nil {
			return AppRegistration{}, fmt.Errorf("cannot save %s; inspect the existing App before repeating registration", file.name)
		}
	}
	return result.AppRegistration, nil
}

func privateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = io.Copy(file, strings.NewReader(string(data))); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Setup) fail(err error) {
	select {
	case s.failure <- err:
	default:
	}
}
