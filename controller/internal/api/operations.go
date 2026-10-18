package api

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/store"
)

func (s *Server) refresh(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if s.CI != nil {
		s.CI.RequestRefresh()
	}
	if err := s.Rooms.Refresh(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Rooms.Branches(r.Context())
	if err != nil {
		return err
	}
	return respond(w, 200, value)
}

func (s *Server) commits(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	q := r.URL.Query()
	branch := q.Get("branch")
	if branch == "" {
		branch = s.Cfg.Git.DefaultBranch
	}
	limit := 50
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n <= 0 {
			return model.Failure("invalid_request", "limit 必须为正整数")
		}
		limit = n
	}
	values, err := s.Git.Commits(r.Context(), branch, q.Get("before"), limit)
	if err != nil {
		return err
	}
	for i := range values {
		values[i].Project = s.Cfg.ID
		values[i].Builds = map[string]*model.BuildSummary{}
		for _, loader := range s.Cfg.EnabledLoaders() {
			values[i].Builds[loader] = nil
			var b model.Build
			err = s.DB.Get(r.Context(), "build", rooms.BuildID(values[i].SHA, loader), &b)
			if err == nil {
				values[i].Builds[loader] = &model.BuildSummary{ID: b.ID, Status: b.Status}
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
	}
	return respond(w, 200, values)
}

func (s *Server) buildList(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	q := r.URL.Query()
	if sha := q.Get("sha"); sha != "" && !rooms.ValidSHA(sha) {
		return model.Failure("invalid_request", "sha 无效")
	}
	if loader := q.Get("loader"); loader != "" && !s.Cfg.Loaders[loader].Enabled {
		return model.Failure("invalid_request", "loader 无效")
	}
	if status := q.Get("status"); status != "" && !slices.Contains([]string{"queued", "building", "ready", "failed"}, status) {
		return model.Failure("invalid_request", "构建状态无效")
	}
	values, err := s.Builds.List(r.Context())
	if err != nil {
		return err
	}
	result := []model.Build{}
	for _, b := range values {
		if (q.Get("sha") == "" || q.Get("sha") == b.SHA) && (q.Get("loader") == "" || q.Get("loader") == b.Loader) && (q.Get("status") == "" || q.Get("status") == b.Status) {
			result = append(result, b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].QueuedAt.Equal(result[j].QueuedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].QueuedAt.After(result[j].QueuedAt)
	})
	return respond(w, 200, result)
}

func (s *Server) buildCreate(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var req struct {
		SHA    string `json:"sha"`
		Loader string `json:"loader"`
		Force  bool   `json:"force"`
	}
	if err := decode(r, &req, false); err != nil {
		return err
	}
	value, created, err := s.Builds.Request(r.Context(), req.SHA, req.Loader, req.Force)
	if err != nil {
		return err
	}
	status := 200
	if created {
		status = 202
	}
	return respond(w, status, value)
}

func (s *Server) buildGet(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Builds.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return respond(w, 200, value)
}

func (s *Server) buildLog(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	f, err := s.Builds.OpenLog(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64*1024*1024))
	if err != nil {
		return err
	}
	return plain(w, redact(string(data)))
}

func (s *Server) buildFile(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if id.RoomID != "" && id.BuildID != r.PathValue("id") {
		return model.Failure("not_found", "文件不存在")
	}
	f, err := s.Builds.OpenFile(r.Context(), r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		return err
	}
	return serveFile(w, r, f, "application/java-archive")
}

func (s *Server) roomList(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	q := r.URL.Query()
	for key, allowed := range map[string]string{"kind": "branch commit world pr", "state": "stopped queued building starting ready stopping archiving closed failed lost"} {
		if value := q.Get(key); value != "" && !slices.Contains(strings.Fields(allowed), value) {
			return model.Failure("invalid_request", "房间过滤条件无效")
		}
		if len(q[key]) > 1 {
			return model.Failure("invalid_request", "房间过滤条件必须为单值")
		}
	}
	all, err := s.Rooms.List(r.Context())
	if err != nil {
		return err
	}
	result := []model.Room{}
	for _, room := range all {
		if (q.Get("kind") == "" || q.Get("kind") == room.Kind) && (q.Get("state") == "" || q.Get("state") == room.State) {
			result = append(result, room)
		}
	}
	return respond(w, 200, result)
}

func (s *Server) roomGet(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Rooms.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if id.RoomID != "" && value.ID != id.RoomID {
		leases, err := store.All[model.LeaseRecord](r.Context(), s.DB, "lease")
		if err != nil {
			return err
		}
		allowed := false
		for _, lease := range leases {
			if lease.RoomID == value.ID && lease.Generation == value.Generation && lease.FromRoom == id.RoomID && lease.FromGeneration == id.Generation && lease.ExpiresAt.After(time.Now()) {
				allowed = true
			}
		}
		if !allowed {
			return model.Failure("not_found", "房间不存在")
		}
	}
	return respond(w, 200, value)
}

func (s *Server) roomCreate(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var req struct {
		Kind   string `json:"kind"`
		SHA    string `json:"sha"`
		Loader string `json:"loader"`
		Seed   string `json:"seed"`
		World  string `json:"world"`
	}
	if err := decode(r, &req, false); err != nil {
		return err
	}
	room, created, err := s.Rooms.Create(r.Context(), req.Kind, req.SHA, req.Loader, req.Seed, req.World)
	if err != nil {
		return err
	}
	status := 200
	if created {
		status = 201
	}
	return respond(w, status, room)
}

func (s *Server) roomStart(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var req struct {
		Seed *string `json:"seed"`
	}
	if err := decode(r, &req, true); err != nil {
		return err
	}
	result, status, err := s.Rooms.Start(r.Context(), r.PathValue("id"), req.Seed, id)
	if err != nil {
		return err
	}
	return respond(w, status, result)
}

func (s *Server) roomStop(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	return s.stop(w, r, false)
}

func (s *Server) roomRestart(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	return s.stop(w, r, true)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request, restart bool) error {
	room, status, err := s.Rooms.Stop(r.Context(), r.PathValue("id"), restart)
	if err != nil {
		return err
	}
	return respond(w, status, room)
}

func (s *Server) roomLabel(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var req struct {
		Label string `json:"label"`
	}
	if err := decode(r, &req, false); err != nil {
		return err
	}
	room, status, err := s.Rooms.Label(r.Context(), r.PathValue("id"), req.Label)
	if err != nil {
		return err
	}
	return respond(w, status, room)
}

func (s *Server) roomDelete(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if err := s.Rooms.Delete(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) leaseDelete(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if err := s.Rooms.DeleteLease(r.Context(), r.PathValue("id"), r.PathValue("lease"), id); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) roomLog(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Rooms.Log(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return plain(w, redact(value))
}

func (s *Server) destinations(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Rooms.Destinations(r.Context(), r.URL.Query().Get("fromRoom"), id)
	if err != nil {
		return err
	}
	return respond(w, 200, value)
}

func (s *Server) hop(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var body struct {
		FromRoom string       `json:"fromRoom"`
		Dest     string       `json:"dest"`
		Player   model.Player `json:"player"`
	}
	if err := decode(r, &body, false); err != nil {
		return err
	}
	value, status, err := s.Rooms.Hop(r.Context(), body.FromRoom, body.Dest, body.Player, id)
	if err != nil {
		return err
	}
	return respond(w, status, value)
}

var packPublishedName = regexp.MustCompile(`^[0-9a-f]{64}\.zip$`)

func (s *Server) packFile(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	name := r.PathValue("file")
	if !packPublishedName.MatchString(name) {
		return model.Failure("invalid_request", "资源包文件名无效")
	}
	f, err := s.Rooms.Worlds.OpenPack(r.Context(), r.PathValue("id"), strings.TrimSuffix(name, ".zip"))
	if err != nil {
		return err
	}
	return serveFile(w, r, f, "application/zip")
}

func (s *Server) packList(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Rooms.Worlds.Packs(r.Context())
	if err != nil {
		return err
	}
	return respond(w, 200, value)
}
