package worlds

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/filehash"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
)

func copyImmutable(source, destination, digest string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(destination), ".publish-")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	_, writeErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	valid, err := intact(output.Name(), digest, size)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("published source changed while copying")
	}
	return os.Rename(output.Name(), destination)
}

func writeMetadata(destination string, value any) (string, int64, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", 0, err
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return "", 0, err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", 0, err
	}
	_, writeErr := output.Write(append(data, '\n'))
	syncErr := output.Sync()
	closeErr := output.Close()
	if writeErr != nil {
		return "", 0, writeErr
	}
	if syncErr != nil {
		return "", 0, syncErr
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	return worldarchive.Digest(destination)
}

func writeRevisionMetadata(destination string, revision model.WorldRevision) (string, int64, error) {
	value := struct {
		model.WorldMetadata
		Project    string          `json:"project"`
		Rev        string          `json:"rev"`
		SnapshotID *string         `json:"snapshotId"`
		World      model.WorldFile `json:"world"`
		CreatedAt  time.Time       `json:"createdAt"`
	}{revision.WorldMetadata, revision.Project, revision.Rev, revision.SnapshotID, revision.World, revision.CreatedAt}
	return writeMetadata(destination, value)
}

func (m *Manager) queue(ctx context.Context, tx *store.Tx, kind, id, revision, part, relative, digest string, size int64) error {
	key := model.TransferID(kind, id, revision, part)
	remote := path.Join("projects", m.cfg.ID, relative)
	var task model.Transfer
	err := tx.Get(ctx, "transfer", key, &task)
	if err == nil {
		if task.SHA256 != digest || task.Size != size || task.LocalPath != relative || task.RemotePath != remote {
			return model.Failure("conflict", "published transfer metadata changed")
		}
		if task.Status != "failed" {
			return nil
		}
		valid, err := intact(m.absolute(relative), digest, size)
		if err != nil {
			return err
		}
		if !valid {
			return model.Failure("archive_failed", "failed upload has no intact local file")
		}
		task.Status, task.LastError, task.UpdatedAt = "queued", nil, time.Now().UTC()
		return tx.Put(ctx, "transfer", key, task)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	now := time.Now().UTC()
	task = model.Transfer{Project: m.cfg.ID, ID: key, Kind: kind, ObjectID: id, Revision: revision, Part: part, LocalPath: relative, RemotePath: remote, SHA256: digest, Size: size, Status: "queued", CreatedAt: now, UpdatedAt: now}
	return tx.Put(ctx, "transfer", key, task)
}

func (m *Manager) queuePack(ctx context.Context, tx *store.Tx, value model.PackRevision) error {
	digest, size, err := worldarchive.Digest(m.absolute(PackMetadataPath(value.ID, value.Rev)))
	if err != nil {
		return err
	}
	if err = m.queue(ctx, tx, "pack", value.ID, value.Rev, "zip", value.Path, value.SHA256, value.Size); err != nil {
		return err
	}
	return m.queue(ctx, tx, "pack", value.ID, value.Rev, "metadata", PackMetadataPath(value.ID, value.Rev), digest, size)
}

func (m *Manager) queueRevision(ctx context.Context, tx *store.Tx, value model.WorldRevision) error {
	digest, size, err := worldarchive.Digest(m.absolute(MetadataPath(value.ID, value.Rev)))
	if err != nil {
		return err
	}
	if err = m.queue(ctx, tx, "world", value.ID, value.Rev, "world", value.World.Path, value.World.SHA256, value.World.Size); err != nil {
		return err
	}
	if err = m.queue(ctx, tx, "world", value.ID, value.Rev, "metadata", MetadataPath(value.ID, value.Rev), digest, size); err != nil {
		return err
	}
	if value.ResourcePack != nil {
		var pack model.PackRevision
		if err = tx.Get(ctx, "pack_revision", value.ResourcePack.ID+":"+value.ResourcePack.Rev, &pack); err != nil {
			return missing(err)
		}
		return m.queuePack(ctx, tx, pack)
	}
	return nil
}

func (m *Manager) Publish(ctx context.Context, metadata model.WorldMetadata, source string) (model.WorldRevision, bool, error) {
	return m.publish(ctx, metadata, source, nil, metadata.Requires.Namespaces != nil)
}

func (m *Manager) publish(ctx context.Context, metadata model.WorldMetadata, source string, snapshotID *string, declaredNamespaces bool) (model.WorldRevision, bool, error) {
	var result model.WorldRevision
	metadata, err := m.metadata(metadata)
	if err != nil {
		return result, false, err
	}
	if err = worldarchive.Space(m.cfg.DataDir); err != nil {
		return result, false, err
	}
	if err = worldarchive.Validate(source, metadata.Minecraft, m.cfg.Worlds.ExtraRootEntries...); err != nil {
		return result, false, model.Failure("invalid_request", err.Error())
	}
	namespaces, err := m.scan(ctx, source, metadata.Minecraft)
	if err != nil {
		return result, false, err
	}
	if declaredNamespaces && !slices.Equal(namespaces, metadata.Requires.Namespaces) {
		return result, false, model.Failure("invalid_request", "declared namespaces differ from the actual world")
	}
	metadata.Requires.Namespaces = namespaces
	hashes, err := filehash.File(source)
	if err != nil {
		return result, false, err
	}
	digest, size := hashes.SHA256, hashes.Size
	created := false
	err = m.db.Update(ctx, func(tx *store.Tx) error {
		var world model.World
		err := tx.Get(ctx, "world", metadata.ID, &world)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil {
			if restoring(world) {
				return model.Failure("conflict", "world restoration is in progress")
			}
			if err = tx.Get(ctx, "world_revision", world.ID+":"+world.LatestRev, &result); err != nil {
				return err
			}
			if result.World.SHA256 == digest {
				valid, err := intact(m.absolute(result.World.Path), digest, size)
				if err != nil {
					return err
				}
				if !valid {
					if err = copyImmutable(source, m.absolute(result.World.Path), digest, size); err != nil {
						return err
					}
				}
				if _, err = os.Stat(m.absolute(MetadataPath(result.ID, result.Rev))); errors.Is(err, os.ErrNotExist) {
					if _, _, err = writeRevisionMetadata(m.absolute(MetadataPath(result.ID, result.Rev)), result); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
				return m.queueRevision(ctx, tx, result)
			}
		} else {
			world = model.World{Project: m.cfg.ID, ID: metadata.ID, State: "active"}
		}
		if metadata.ResourcePack != nil {
			var pack model.PackRevision
			if err = tx.Get(ctx, "pack_revision", metadata.ResourcePack.ID+":"+metadata.ResourcePack.Rev, &pack); err != nil {
				return missing(err)
			}
			valid, err := intact(m.absolute(PackPath(pack.ID, pack.Rev)), pack.SHA256, pack.Size)
			if err != nil {
				return err
			}
			if !valid {
				return model.Failure("archive_failed", "referenced resource pack is not locally available")
			}
		}
		number, err := tx.Next(ctx, "world_revision:"+metadata.ID)
		if err != nil {
			return err
		}
		revision := strconv.Itoa(number)
		result = model.WorldRevision{Project: m.cfg.ID, WorldMetadata: metadata, Rev: revision, SnapshotID: snapshotID, World: model.WorldFile{SHA256: digest, SHA1: hashes.SHA1, SHA512: hashes.SHA512, Path: WorldPath(metadata.ID, revision), Size: size, URL: downloadURL(m.cfg.ID, metadata.ID, revision)}, CreatedAt: time.Now().UTC()}
		if err = copyImmutable(source, m.absolute(WorldPath(metadata.ID, revision)), digest, size); err != nil {
			return err
		}
		if _, _, err = writeRevisionMetadata(m.absolute(MetadataPath(metadata.ID, revision)), result); err != nil {
			return err
		}
		world.Title, world.LatestRev, world.Minecraft, world.Requires, world.ResourcePack = metadata.Title, revision, metadata.Minecraft, metadata.Requires, metadata.ResourcePack
		if err = m.queueRevision(ctx, tx, result); err != nil {
			return err
		}
		if err = tx.Put(ctx, "world_revision", metadata.ID+":"+revision, result); err != nil {
			return err
		}
		if err = tx.Put(ctx, "world", metadata.ID, world); err != nil {
			return err
		}
		created = true
		return nil
	})
	return result, created, err
}

func (m *Manager) Promote(ctx context.Context, id, snapshotID string) (model.WorldRevision, bool, error) {
	var result model.WorldRevision
	if !config.ValidID(id) {
		return result, false, model.Failure("invalid_request", "world ID is invalid")
	}
	var snapshot model.Snapshot
	if err := m.db.Get(ctx, "snapshot", snapshotID, &snapshot); err != nil {
		return result, false, missing(err)
	}
	if snapshot.World == nil || snapshot.World.ID != id {
		return result, false, model.Failure("invalid_request", "snapshot was not captured from this world")
	}
	if !snapshot.LocalAvailable {
		return result, false, model.Failure("archive_failed", "snapshot requires restoration")
	}
	origin, err := m.Revision(ctx, id, snapshot.World.Rev)
	if err != nil {
		return result, false, err
	}
	if !filepath.IsLocal(snapshot.Path) {
		return result, false, fmt.Errorf("invalid recorded snapshot path")
	}
	source := m.absolute(snapshot.Path)
	valid, err := intact(source, snapshot.SHA256, snapshot.Size)
	if err != nil {
		return result, false, err
	}
	if !valid {
		return result, false, model.Failure("archive_failed", "snapshot checksum differs from its published metadata")
	}
	return m.publish(ctx, origin.WorldMetadata, source, &snapshotID, false)
}

func validatePack(filename string) error {
	reader, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer reader.Close()
	seen, metadataFound := map[string]bool{}, false
	var total uint64
	for _, entry := range reader.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || path.Clean(name) != name || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") || entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid pack ZIP path")
		}
		if seen[name] {
			return fmt.Errorf("duplicate pack ZIP path")
		}
		seen[name] = true
		if !entry.Mode().IsRegular() && !entry.FileInfo().IsDir() {
			return fmt.Errorf("resource pack contains a special file")
		}
		if entry.UncompressedSize64 > 2*1024*1024*1024 || total > 2*1024*1024*1024-entry.UncompressedSize64 {
			return fmt.Errorf("resource pack exceeds expanded size limit")
		}
		total += entry.UncompressedSize64
		if entry.FileInfo().IsDir() {
			continue
		}
		if name != "pack.mcmeta" {
			file, err := entry.Open()
			if err != nil {
				return err
			}
			_, readErr := io.Copy(io.Discard, file)
			closeErr := file.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			continue
		}
		if entry.UncompressedSize64 > 1024*1024 {
			return fmt.Errorf("pack.mcmeta exceeds size limit")
		}
		file, err := entry.Open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		var metadata struct {
			Pack map[string]json.RawMessage `json:"pack"`
		}
		if err = json.Unmarshal(data, &metadata); err != nil {
			return err
		}
		if len(metadata.Pack["description"]) == 0 || len(metadata.Pack["pack_format"]) == 0 && (len(metadata.Pack["min_format"]) == 0 || len(metadata.Pack["max_format"]) == 0) {
			return fmt.Errorf("pack.mcmeta lacks pack description or format")
		}
		if string(metadata.Pack["description"]) == "null" {
			return fmt.Errorf("pack description cannot be null")
		}
		for _, key := range []string{"pack_format", "min_format", "max_format"} {
			if value, found := metadata.Pack[key]; found {
				var version any
				if err = json.Unmarshal(value, &version); err != nil {
					return err
				}
				switch number := version.(type) {
				case float64:
					if number < 0 || number != float64(int64(number)) {
						return fmt.Errorf("invalid pack format")
					}
				case []any:
					if key == "pack_format" || len(number) != 2 {
						return fmt.Errorf("invalid pack format range")
					}
					for _, part := range number {
						n, ok := part.(float64)
						if !ok || n < 0 || n != float64(int64(n)) {
							return fmt.Errorf("invalid pack format range")
						}
					}
				default:
					return fmt.Errorf("invalid pack format")
				}
			}
		}
		metadataFound = true
	}
	if !metadataFound {
		return fmt.Errorf("resource pack has no pack.mcmeta")
	}
	return nil
}

func (m *Manager) PublishPack(ctx context.Context, id, source string) (model.PackRevision, bool, error) {
	var result model.PackRevision
	if !validPackID(id) {
		return result, false, model.Failure("invalid_request", "pack ID is invalid")
	}
	if err := worldarchive.Space(m.cfg.DataDir); err != nil {
		return result, false, err
	}
	if err := validatePack(source); err != nil {
		return result, false, model.Failure("invalid_request", err.Error())
	}
	hashes, err := filehash.File(source)
	if err != nil {
		return result, false, err
	}
	digest, size := hashes.SHA256, hashes.Size
	created := false
	err = m.db.Update(ctx, func(tx *store.Tx) error {
		err := tx.Get(ctx, "pack_revision", id+":"+digest, &result)
		if err == nil {
			valid, err := intact(m.absolute(PackPath(id, digest)), digest, size)
			if err != nil {
				return err
			}
			if !valid {
				if err = copyImmutable(source, m.absolute(PackPath(id, digest)), digest, size); err != nil {
					return err
				}
			}
			if _, err = os.Stat(m.absolute(PackMetadataPath(id, digest))); errors.Is(err, os.ErrNotExist) {
				if _, _, err = writeMetadata(m.absolute(PackMetadataPath(id, digest)), result); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			return m.queuePack(ctx, tx, result)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		result = model.PackRevision{Project: m.cfg.ID, ID: id, Rev: digest, SHA256: digest, SHA1: hashes.SHA1, SHA512: hashes.SHA512, Path: PackPath(id, digest), Size: size, URL: "/v2/projects/" + url.PathEscape(m.cfg.ID) + "/packs/" + url.PathEscape(id) + "/" + digest + ".zip", CreatedAt: time.Now().UTC()}
		if err = copyImmutable(source, m.absolute(PackPath(id, digest)), digest, size); err != nil {
			return err
		}
		if _, _, err = writeMetadata(m.absolute(PackMetadataPath(id, digest)), result); err != nil {
			return err
		}
		if err = m.queuePack(ctx, tx, result); err != nil {
			return err
		}
		if err = tx.Put(ctx, "pack_revision", id+":"+digest, result); err != nil {
			return err
		}
		created = true
		return nil
	})
	return result, created, err
}
