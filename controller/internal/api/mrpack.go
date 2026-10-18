package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/mrpack"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
)

func (s *Server) pulls(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if s.CI == nil {
		return respond(w, 200, []model.Pull{})
	}
	values, err := s.CI.List(r.Context())
	if err != nil {
		return err
	}
	return respond(w, 200, values)
}

func (s *Server) buildMRPack(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Builds.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return s.exportMRPack(w, r, value, nil)
}

func (s *Server) roomMRPack(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	query := r.URL.Query()
	buildID, generation := query.Get("buildId"), query.Get("generation")
	pinned := query.Has("buildId") || query.Has("generation")
	var number int64
	if pinned {
		var err error
		number, err = strconv.ParseInt(generation, 10, 64)
		if err != nil || number < 1 || buildID == "" || len(query["buildId"]) != 1 || len(query["generation"]) != 1 {
			return model.Failure("invalid_request", "房间包需要同时指定 buildId 和有效 generation")
		}
	}
	room, value, err := s.Rooms.PackTarget(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if pinned {
		if room.BuildID != buildID || room.Generation != number {
			return model.Failure("build_mismatch", "房间构建或启动周期已变化，请重新加入")
		}
	}
	return s.exportMRPack(w, r, value, &room)
}

func (s *Server) exportMRPack(w http.ResponseWriter, r *http.Request, value model.Build, room *model.Room) error {
	if err := worldarchive.Space(s.Cfg.DataDir); err != nil {
		return err
	}
	directory := filepath.Join(s.Cfg.DataDir, ".work", "mrpack")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, "pack-*.mrpack")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	err = mrpack.Export(r.Context(), file, value, room, func(ctx context.Context, entry model.BuildFile) (*os.File, error) {
		return s.Builds.OpenFile(ctx, value.ID, entry.Name)
	})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+value.ID+".mrpack\"")
	w.Header().Set("Cache-Control", "private, no-store")
	return serveFile(w, r, file, mrpack.ContentType)
}
