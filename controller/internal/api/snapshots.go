package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/imdomestic/chorus-testbed/controller/internal/worldarchive"
)

func (s *Server) snapshotList(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	all, err := store.All[model.Snapshot](r.Context(), s.DB, "snapshot")
	if err != nil {
		return err
	}
	result := []model.Snapshot{}
	for _, snapshot := range all {
		if room := r.URL.Query().Get("room"); room == "" || room == snapshot.RoomID {
			result = append(result, snapshot)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CapturedAt.Equal(result[j].CapturedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CapturedAt.After(result[j].CapturedAt)
	})
	return respond(w, 200, result)
}

func (s *Server) snapshotGet(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var snapshot model.Snapshot
	if err := s.DB.Get(r.Context(), "snapshot", r.PathValue("id"), &snapshot); err != nil {
		return err
	}
	return respond(w, 200, snapshot)
}

func (s *Server) snapshotDownload(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	f, err := s.Rooms.Archive.OpenSnapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return serveFile(w, r, f, "application/gzip")
}

func (s *Server) snapshotRestore(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var body struct{}
	if err := decode(r, &body, true); err != nil {
		return err
	}
	snapshot, accepted, err := s.Rooms.Archive.RequestSnapshotRestore(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	status := 200
	if accepted {
		status = 202
	}
	return respond(w, status, snapshot)
}

func (s *Server) snapshotUpload(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	q := r.URL.Query()
	generation, err := strconv.ParseInt(q.Get("generation"), 10, 64)
	if err != nil || generation != id.Generation || q.Get("room") != id.RoomID || q.Get("build") != id.BuildID {
		return model.Failure("unauthorized", "快照与房间凭据不匹配")
	}
	if r.Header.Get("Content-Type") != "application/gzip" {
		return model.Failure("invalid_request", "快照必须使用 application/gzip")
	}
	if err = worldarchive.Space(s.Cfg.Server.DataDir); err != nil {
		return model.Failure("archive_failed", err.Error())
	}
	work := filepath.Join(s.Cfg.Server.DataDir, ".work")
	if err = os.MkdirAll(work, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(work, "snapshot-upload-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, copyErr := io.Copy(f, http.MaxBytesReader(w, r.Body, 32*1024*1024*1024))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = worldarchive.Validate(f.Name(), s.Cfg.Minecraft.Version); err != nil {
		return model.Failure("invalid_request", err.Error())
	}
	sha, size, err := worldarchive.Digest(f.Name())
	if err != nil {
		return err
	}
	snapshot := model.Snapshot{ID: fmt.Sprintf("%s-g%d", id.RoomID, id.Generation), RoomID: id.RoomID, RoomGeneration: id.Generation, BuildID: id.BuildID, Minecraft: s.Cfg.Minecraft.Version, SHA256: sha, Size: size, CreatedAt: time.Now().UTC(), LocalAvailable: true}
	snapshot, created, err := s.Rooms.PublishSnapshot(r.Context(), snapshot, f.Name())
	if err != nil {
		return err
	}
	status := 200
	if created {
		status = 201
	}
	return respond(w, status, snapshot)
}
