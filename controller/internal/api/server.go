package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/ci"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/events"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/projectstate"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/store"
)

type Server struct {
	Global       config.Config
	Cfg          config.ProjectConfig
	DB           *store.Store
	Git          *gitmirror.Mirror
	Builds       *build.Manager
	CI           *ci.Manager
	Rooms        *rooms.Manager
	State        *projectstate.State
	Auth         *auth.Auth
	Device       *auth.DeviceFlow
	Events       *events.Bus
	Projects     map[string]*Server
	RouterSource func(context.Context, string) (bool, error)
}

func New(cfg config.Config, projects map[string]*Server, a *auth.Auth, e *events.Bus) *Server {
	s := &Server{Global: cfg, Projects: projects, Auth: a, Events: e}
	for _, project := range projects {
		project.Global, project.Auth, project.Events = cfg, a, e
	}
	return s
}

type handler func(*Server, http.ResponseWriter, *http.Request, auth.Identity) error

type Route struct {
	Method     string
	Path       string
	Permission string
	handle     handler
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, route := range Routes() {
		mux.HandleFunc(route.Method+" "+route.Path, func(w http.ResponseWriter, r *http.Request) {
			target := s
			if project := r.PathValue("p"); project != "" {
				var found bool
				target, found = s.Projects[project]
				if !found {
					writeError(w, model.Failure("not_found", "项目不存在"))
					return
				}
			}
			identity, err := target.authorize(r, route.Permission)
			if err == nil && target.Cfg.ID != "" && (route.Method != http.MethodGet || strings.HasSuffix(route.Path, "/pack.mrpack")) {
				err = target.State.Check()
			}
			if err == nil {
				err = route.handle(target, w, r, identity)
			}
			if err != nil {
				writeError(w, err)
			}
		})
	}
	return mux
}

func (s *Server) authorize(r *http.Request, permission string) (auth.Identity, error) {
	var identity auth.Identity
	if permission == "public" {
		return identity, nil
	}
	if permission == "optional-user" && r.Header.Get("Authorization") == "" {
		return identity, nil
	}
	if permission == "internal" {
		if s.trustedWebhook(r) {
			return identity, nil
		}
		return identity, model.Failure("unauthorized", "内部来源无效")
	}
	if (permission == "download" || permission == "viewer-download" || permission == "developer-download" || permission == "seed-download" || permission == "pack-download") && s.Auth.ValidDownload(r) {
		return identity, nil
	}
	identity, err := s.Auth.Authenticate(r)
	if errors.Is(err, auth.ErrUnauthenticated) {
		return identity, model.Failure("unauthorized", "请求凭据无效")
	}
	if err != nil {
		return identity, err
	}
	if identity.RoomID != "" {
		if s.Cfg.ID == "" || identity.Project != s.Cfg.ID {
			return identity, model.Failure("not_found", "项目不存在")
		}
		if permission != "room" && permission != "room-or-viewer" && permission != "download" && permission != "seed-download" {
			return identity, model.Failure("forbidden", "房间凭据不能执行此操作")
		}
		return identity, s.currentRoomIdentity(r.Context(), identity)
	}
	if identity.User == "" || permission == "room" {
		return identity, model.Failure("unauthorized", "需要用户或房间凭据")
	}
	if permission == "admin" {
		user, found := s.Auth.User(identity.User)
		if !found || !user.Admin {
			return identity, model.Failure("forbidden", "需要管理员权限")
		}
	}
	if s.Cfg.ID != "" {
		role := config.Viewer
		if permission == "developer" || permission == "developer-download" || permission == "seed-download" {
			role = config.Developer
		}
		if permission == "owner" {
			role = config.Owner
		}
		return identity, s.Auth.RequireProject(identity, s.Cfg.ID, role)
	}
	return identity, nil
}

func (s *Server) trustedWebhook(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || s.RouterSource == nil {
		return false
	}
	ip := net.ParseIP(host)
	for _, source := range s.Global.Runtime.Kubernetes.WebhookSources {
		_, network, err := net.ParseCIDR(source)
		if err == nil && network.Contains(ip) || source == host {
			trusted, err := s.RouterSource(r.Context(), host)
			return err == nil && trusted
		}
	}
	return false
}

func (s *Server) currentRoomIdentity(ctx context.Context, id auth.Identity) error {
	room, err := s.Rooms.Get(ctx, id.RoomID)
	if err != nil || room.Project != id.Project || room.Generation != id.Generation || room.BuildID != id.BuildID || room.State == "closed" {
		return model.Failure("unauthorized", "房间凭据周期无效")
	}
	return nil
}

func decode(r *http.Request, out any, optional bool) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(out)
	if err == io.EOF && optional {
		return nil
	}
	if err != nil {
		return model.Failure("invalid_request", "JSON 请求格式无效")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return model.Failure("invalid_request", "JSON 请求包含额外内容")
	}
	return nil
}

func respond(w http.ResponseWriter, status int, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err = w.Write(append(data, '\n'))
	return err
}

func writeError(w http.ResponseWriter, err error) {
	status := 500
	var business *model.Error
	if errors.As(err, &business) {
		switch business.Code {
		case "invalid_request", "invite_invalid", "expired_token":
			status = 400
		case "rate_limited":
			status = 429
		case "unauthorized":
			status = 401
		case "forbidden", "access_denied", "account_not_linked":
			status = 403
		case "provider_unavailable", "project_initializing":
			status = 503
		case "not_found":
			status = 404
		default:
			status = 409
		}
	} else if errors.Is(err, store.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		status = 404
		business = model.Failure("not_found", "请求对象不存在")
	}
	if business == nil {
		slog.Error("API operation failed", "error", err)
		business = model.Failure("internal_error", "服务端操作失败")
	}
	_ = respond(w, status, struct {
		Error *model.Error `json:"error"`
	}{business})
}

func plain(w http.ResponseWriter, value string) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, err := io.WriteString(w, value)
	return err
}

var signedQuery = regexp.MustCompile(`([?&](?:sig|token)=)[^&\s"']+`)
var bearer = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._~-]+`)

func redact(text string) string {
	return bearer.ReplaceAllString(signedQuery.ReplaceAllString(text, "${1}[redacted]"), "${1}[redacted]")
}

func serveFile(w http.ResponseWriter, r *http.Request, f *os.File, contentType string) error {
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
	return nil
}
