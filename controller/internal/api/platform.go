package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/events"
	"github.com/imdomestic/vex/controller/internal/model"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	return respond(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	methods := model.AuthMethods{Methods: []string{"invite"}}
	if s.Device != nil {
		methods.Methods = append(methods.Methods, "github")
	}
	if id.User == "" {
		return respond(w, 200, struct {
			Name    string            `json:"name"`
			Version string            `json:"version"`
			Auth    model.AuthMethods `json:"auth"`
		}{s.Global.Server.Name, "0.3.0", methods})
	}
	value := model.Meta{Name: s.Global.Server.Name, Version: "0.3.0", Zone: s.Global.Server.Zone, PublicBaseURL: s.Global.Server.PublicBaseURL, Runtimes: map[string]model.RuntimeCatalog{}, Agents: []model.AgentVersion{}, Auth: methods}
	for name, runtime := range s.Global.Runtimes {
		java, err := strconv.Atoi(strings.TrimPrefix(name, "java"))
		if err != nil {
			return err
		}
		value.Runtimes[name] = model.RuntimeCatalog{Java: java, ServerImage: runtime.ServerImage}
	}
	for _, agent := range s.Global.Agents {
		value.Agents = append(value.Agents, model.AgentVersion{Minecraft: agent.Minecraft, Loader: agent.Loader, Version: agent.Version})
	}
	sort.Slice(value.Agents, func(i, j int) bool {
		return value.Agents[i].Minecraft+value.Agents[i].Loader+value.Agents[i].Version < value.Agents[j].Minecraft+value.Agents[j].Loader+value.Agents[j].Version
	})
	return respond(w, 200, value)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	user, ok := s.Auth.User(id.User)
	if !ok {
		return model.Failure("unauthorized", "用户不存在")
	}
	return respond(w, 200, user)
}

func (s *Server) projectValue(id auth.Identity) model.Project {
	role := s.Cfg.Members[id.User]
	if user, ok := s.Auth.User(id.User); ok && user.Admin {
		role = config.Owner
	}
	return model.Project{ID: s.Cfg.ID, Remote: s.Cfg.Git.Remote, DefaultBranch: s.Cfg.Git.DefaultBranch, Role: string(role), Minecraft: s.Cfg.Minecraft.Version, Java: s.Cfg.Minecraft.Java, Loaders: s.Cfg.EnabledLoaders(), Policy: map[string]any{"trust": s.Cfg.Trust, "buildSource": s.Cfg.Build.Source, "acceptEULA": s.Cfg.Minecraft.AcceptEULA}, Quota: map[string]any{"maxRooms": s.Cfg.Quota.MaxRooms, "roomMemoryMax": s.Cfg.Quota.RoomMemoryMax, "buildCache": s.Cfg.Quota.BuildCache, "snapshotKeep": s.Cfg.Quota.SnapshotKeep, "snapshotKeepDays": s.Cfg.Quota.SnapshotKeepDays}}
}

func (s *Server) projectList(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	result := []model.Project{}
	for _, project := range s.Auth.VisibleProjects(id) {
		result = append(result, s.Projects[project].projectValue(id))
	}
	return respond(w, 200, result)
}

func (s *Server) projectGet(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	return respond(w, 200, s.projectValue(id))
}

func (s *Server) nodes(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	for _, project := range s.Projects {
		value, err := project.Rooms.Nodes(r.Context())
		if err != nil {
			return err
		}
		return respond(w, 200, value)
	}
	return respond(w, 200, []model.Node{})
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var event struct {
		Event     string          `json:"event"`
		Timestamp string          `json:"timestamp"`
		Status    string          `json:"status"`
		Server    string          `json:"server"`
		Player    json.RawMessage `json:"player"`
		Backend   json.RawMessage `json:"backend"`
		Client    json.RawMessage `json:"client"`
		Error     json.RawMessage `json:"error"`
	}
	if err := decode(r, &event, false); err != nil {
		return err
	}
	for _, project := range s.Projects {
		project.Rooms.RequestReconcile(event.Server)
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	projects := s.Auth.VisibleProjects(id)
	if s.Cfg.ID != "" {
		projects = []string{s.Cfg.ID}
	}
	if filter := r.URL.Query().Get("project"); filter != "" {
		selected := strings.Split(filter, ",")
		for _, project := range selected {
			if !slices.Contains(projects, project) {
				return model.Failure("not_found", "项目不存在")
			}
		}
		projects = selected
	}
	first, ch, cancel, err := s.Events.Subscribe(r.Context(), projects, r.PathValue("id"))
	if err != nil {
		return err
	}
	defer cancel()
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("HTTP streaming is unavailable")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(e events.Event) error {
		_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Name, e.Data)
		flusher.Flush()
		return err
	}
	if err = write(first); err != nil {
		return nil
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case event, open := <-ch:
			if !open || write(event) != nil {
				return nil
			}
		case <-ticker.C:
			if _, err := s.Auth.Authenticate(r); err != nil {
				return nil
			}
			if _, err = io.WriteString(w, ": ping\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}
