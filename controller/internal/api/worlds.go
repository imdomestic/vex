package api

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func (s *Server) worldPublish(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if err := worldarchive.Space(s.Cfg.DataDir); err != nil {
		return model.Failure("archive_failed", err.Error())
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024*1024*1024+1024*1024)
	reader, err := r.MultipartReader()
	if err != nil {
		return model.Failure("invalid_request", "世界发布需要 multipart/form-data")
	}
	file, err := os.CreateTemp(filepath.Join(s.Cfg.DataDir, ".work"), "world-upload-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	var metadata model.WorldMetadata
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return model.Failure("invalid_request", err.Error())
		}
		name := part.FormName()
		if seen[name] {
			part.Close()
			return model.Failure("invalid_request", "multipart 字段重复")
		}
		seen[name] = true
		mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			part.Close()
			return model.Failure("invalid_request", "multipart 内容类型无效")
		}
		switch name {
		case "metadata":
			if mediaType != "application/json" {
				part.Close()
				return model.Failure("invalid_request", "metadata 字段需要 application/json")
			}
			decoder := json.NewDecoder(io.LimitReader(part, 1024*1024))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&metadata); err != nil {
				part.Close()
				return model.Failure("invalid_request", "世界元数据 JSON 无效")
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				part.Close()
				return model.Failure("invalid_request", "世界元数据包含额外内容")
			}
		case "world":
			if mediaType != "application/gzip" {
				part.Close()
				return model.Failure("invalid_request", "world 字段需要 application/gzip")
			}
			if _, err = io.Copy(file, part); err != nil {
				part.Close()
				return err
			}
		default:
			part.Close()
			return model.Failure("invalid_request", "未知的 multipart 字段")
		}
		if err = part.Close(); err != nil {
			return err
		}
	}
	if !seen["world"] || !seen["metadata"] || metadata.ID != r.PathValue("id") {
		return model.Failure("invalid_request", "世界字段缺失或 ID 不一致")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	policy, err := s.Rooms.DefaultWorldManifest(r.Context())
	if err != nil {
		return err
	}
	value, created, err := s.Rooms.Worlds.Publish(r.Context(), metadata, file.Name(), policy)
	if err != nil {
		return err
	}
	status := 200
	if created {
		status = 201
	}
	return respond(w, status, value)
}

func (s *Server) worldPromote(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var body struct {
		Snapshot string `json:"snapshot"`
	}
	if err := decode(r, &body, false); err != nil {
		return err
	}
	var snapshot model.Snapshot
	if err := s.DB.Get(r.Context(), "snapshot", body.Snapshot, &snapshot); err != nil {
		return err
	}
	document, err := s.Builds.Manifest(r.Context(), snapshot.ManifestSHA)
	if err != nil {
		return err
	}
	value, created, err := s.Rooms.Worlds.Promote(r.Context(), r.PathValue("id"), body.Snapshot, worlds.ManifestPolicy{SHA: snapshot.ManifestSHA, Document: document})
	if err != nil {
		return err
	}
	status := 200
	if created {
		status = 201
	}
	return respond(w, status, value)
}

func (s *Server) worldState(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var body struct {
		State string `json:"state"`
	}
	if err := decode(r, &body, false); err != nil {
		return err
	}
	value, accepted, err := s.Rooms.Worlds.SetState(r.Context(), r.PathValue("id"), body.State)
	if err != nil {
		return err
	}
	status := 200
	if accepted {
		status = 202
	}
	return respond(w, status, value)
}

func (s *Server) worldRestore(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	var body struct{}
	if err := decode(r, &body, true); err != nil {
		return err
	}
	value, accepted, err := s.Rooms.Worlds.RequestRestore(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	status := 200
	if accepted {
		status = 202
	}
	return respond(w, status, value)
}

func (s *Server) packPublish(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if r.Header.Get("Content-Type") != "application/zip" {
		return model.Failure("invalid_request", "资源包需要 application/zip")
	}
	if err := worldarchive.Space(s.Cfg.DataDir); err != nil {
		return model.Failure("archive_failed", err.Error())
	}
	file, err := os.CreateTemp(filepath.Join(s.Cfg.DataDir, ".work"), "pack-upload-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = io.Copy(file, http.MaxBytesReader(w, r.Body, 8*1024*1024*1024)); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	value, created, err := s.Rooms.Worlds.PublishPack(r.Context(), r.PathValue("id"), file.Name())
	if err != nil {
		return err
	}
	status := 200
	if created {
		status = 201
	}
	return respond(w, status, value)
}

func (s *Server) worldList(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Rooms.Worlds.List(r.Context())
	if err != nil {
		return err
	}
	return respond(w, 200, value)
}

func (s *Server) worldGet(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Rooms.Worlds.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return respond(w, 200, value)
}

func (s *Server) worldRevision(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	value, err := s.Rooms.Worlds.Revision(r.Context(), r.PathValue("id"), r.PathValue("rev"))
	if err != nil {
		return err
	}
	return respond(w, 200, value)
}

func (s *Server) worldDownload(w http.ResponseWriter, r *http.Request, id auth.Identity) error {
	if id.RoomID != "" {
		room, err := s.Rooms.Get(r.Context(), id.RoomID)
		if err != nil {
			return err
		}
		seed := room.SeedResolved
		if seed == nil || seed.Kind != "world" || seed.ID != r.PathValue("id") || seed.Rev != r.PathValue("rev") {
			return model.Failure("not_found", "文件不存在")
		}
	}
	file, err := s.Rooms.Worlds.OpenWorld(r.Context(), r.PathValue("id"), r.PathValue("rev"))
	if err != nil {
		return err
	}
	return serveFile(w, r, file, "application/gzip")
}
