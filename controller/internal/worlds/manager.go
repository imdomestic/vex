package worlds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var namespacePattern = regexp.MustCompile(`^[a-z0-9_.-]+$`)

type Manager struct {
	cfg config.ProjectConfig
	db  *store.Store
}

func New(cfg config.ProjectConfig, db *store.Store) (*Manager, error) {
	if !filepath.IsAbs(cfg.DataDir) || db == nil {
		return nil, fmt.Errorf("worlds require an absolute data directory and database")
	}
	for _, directory := range []string{"worlds", "packs", ".work"} {
		if err := os.MkdirAll(filepath.Join(cfg.DataDir, directory), 0700); err != nil {
			return nil, err
		}
	}
	return &Manager{cfg: cfg, db: db}, nil
}

func missing(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return model.Failure("not_found", "published object does not exist")
	}
	return err
}

func (m *Manager) List(ctx context.Context) ([]model.World, error) {
	values, err := store.All[model.World](ctx, m.db, "world")
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

func (m *Manager) Get(ctx context.Context, id string) (model.World, error) {
	var value model.World
	if !config.ValidID(id) {
		return value, model.Failure("not_found", "world does not exist")
	}
	err := m.db.Get(ctx, "world", id, &value)
	return value, missing(err)
}

func validRevision(revision string) bool {
	number, err := strconv.ParseUint(revision, 10, 64)
	return err == nil && number > 0 && strconv.FormatUint(number, 10) == revision
}

func (m *Manager) Revision(ctx context.Context, id, revision string) (model.WorldRevision, error) {
	var value model.WorldRevision
	if !config.ValidID(id) || !validRevision(revision) {
		return value, model.Failure("not_found", "world revision does not exist")
	}
	err := m.db.Get(ctx, "world_revision", id+":"+revision, &value)
	return value, missing(err)
}

func validPackID(id string) bool {
	return config.ValidID(id)
}

func (m *Manager) Pack(ctx context.Context, id, revision string) (model.PackRevision, error) {
	var value model.PackRevision
	if !validPackID(id) || !sha256Pattern.MatchString(revision) {
		return value, model.Failure("not_found", "pack revision does not exist")
	}
	err := m.db.Get(ctx, "pack_revision", id+":"+revision, &value)
	return value, missing(err)
}

func WorldPath(id, revision string) string { return path.Join("worlds", id, revision, "world.tar.gz") }
func MetadataPath(id, revision string) string {
	return path.Join("worlds", id, revision, "revision.json")
}
func PackPath(id, revision string) string         { return path.Join("packs", id, revision+".zip") }
func PackMetadataPath(id, revision string) string { return path.Join("packs", id, revision+".json") }

func (m *Manager) absolute(relative string) string {
	return filepath.Join(m.cfg.DataDir, filepath.FromSlash(relative))
}

func (m *Manager) OpenWorld(ctx context.Context, id, revision string) (*os.File, error) {
	if !config.ValidID(id) || !validRevision(revision) {
		return nil, model.Failure("not_found", "world revision does not exist")
	}
	var file *os.File
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		var value model.WorldRevision
		if err := tx.Get(ctx, "world_revision", id+":"+revision, &value); err != nil {
			return missing(err)
		}
		var err error
		if !filepath.IsLocal(value.World.Path) {
			return fmt.Errorf("invalid recorded world path")
		}
		file, err = os.Open(m.absolute(value.World.Path))
		if errors.Is(err, os.ErrNotExist) {
			return model.Failure("archive_failed", "world revision requires restoration")
		}
		return err
	})
	if err != nil && file != nil {
		file.Close()
		file = nil
	}
	return file, err
}

func (m *Manager) OpenPack(ctx context.Context, id, revision string) (*os.File, error) {
	if !validPackID(id) || !sha256Pattern.MatchString(revision) {
		return nil, model.Failure("not_found", "pack revision does not exist")
	}
	var file *os.File
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		var value model.PackRevision
		if err := tx.Get(ctx, "pack_revision", id+":"+revision, &value); err != nil {
			return missing(err)
		}
		var err error
		if !filepath.IsLocal(value.Path) {
			return fmt.Errorf("invalid recorded pack path")
		}
		file, err = os.Open(m.absolute(value.Path))
		if errors.Is(err, os.ErrNotExist) {
			return model.Failure("archive_failed", "resource pack requires restoration")
		}
		return err
	})
	if err != nil && file != nil {
		file.Close()
		file = nil
	}
	return file, err
}

func (m *Manager) metadata(value model.WorldMetadata, policy ManifestPolicy) (model.WorldMetadata, error) {
	if !config.ValidID(value.ID) || strings.TrimSpace(value.Title) == "" {
		return value, model.Failure("invalid_request", "world ID or title is invalid")
	}
	if value.Minecraft != policy.Document.Minecraft.Version {
		return value, model.Failure("invalid_request", "world Minecraft version differs from the authoritative manifest")
	}
	if value.Source.Scale == 0 {
		value.Source.Scale = 1
	}
	if (value.Source.Commit != "" && !commitPattern.MatchString(value.Source.Commit)) || value.Source.Scale <= 0 || math.IsNaN(value.Source.Scale) || math.IsInf(value.Source.Scale, 0) {
		return value, model.Failure("invalid_request", "world source commit or scale is invalid")
	}
	if len(value.Requires.Loaders) == 0 {
		return value, model.Failure("invalid_request", "world requires at least one loader")
	}
	value.Requires.Loaders = slices.Clone(value.Requires.Loaders)
	sort.Strings(value.Requires.Loaders)
	value.Requires.Loaders = slices.Compact(value.Requires.Loaders)
	for _, loader := range value.Requires.Loaders {
		if configured, ok := m.cfg.Loaders[loader]; !ok || !configured.Enabled {
			return value, model.Failure("invalid_request", "world loader is not enabled in this project")
		}
		if _, ok := policy.Document.Loaders[loader]; !ok {
			return value, model.Failure("invalid_request", "world loader is absent from the authoritative manifest")
		}
	}
	value.Requires.Namespaces = append([]string{}, value.Requires.Namespaces...)
	sort.Strings(value.Requires.Namespaces)
	value.Requires.Namespaces = slices.Compact(value.Requires.Namespaces)
	for _, namespace := range value.Requires.Namespaces {
		if namespace == "minecraft" || !namespacePattern.MatchString(namespace) {
			return value, model.Failure("invalid_request", "world namespace is invalid")
		}
	}
	value.Requires.Mods = append([]string{}, value.Requires.Mods...)
	sort.Strings(value.Requires.Mods)
	value.Requires.Mods = slices.Compact(value.Requires.Mods)
	for _, id := range value.Requires.Mods {
		if !namespacePattern.MatchString(id) {
			return value, model.Failure("invalid_request", "required mod ID is invalid")
		}
	}
	if value.ResourcePack != nil && (!validPackID(value.ResourcePack.ID) || !sha256Pattern.MatchString(value.ResourcePack.Rev)) {
		return value, model.Failure("invalid_request", "resource pack reference is invalid")
	}
	return value, nil
}

func (m *Manager) scan(ctx context.Context, source, minecraft string) ([]string, error) {
	if m.cfg.Worlds.ScannerFile == "" {
		return nil, fmt.Errorf("worlds.scanner_file is required for publishing")
	}
	command := exec.CommandContext(ctx, m.cfg.Worlds.ScannerFile, "--archive", source, "--minecraft", minecraft, "--work-dir", m.absolute(".work"))
	command.Env = append(os.Environ(), "TMPDIR="+m.absolute(".work"), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.Output()
	if err != nil {
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			return nil, model.Failure("invalid_request", "world validation failed: "+strings.TrimSpace(string(failure.Stderr)))
		}
		return nil, err
	}
	var report struct {
		Minecraft  string   `json:"minecraft"`
		Namespaces []string `json:"namespaces"`
	}
	if err = json.Unmarshal(output, &report); err != nil {
		return nil, err
	}
	if report.Minecraft != minecraft || report.Namespaces == nil {
		return nil, fmt.Errorf("world scanner returned an invalid report")
	}
	sort.Strings(report.Namespaces)
	return slices.Compact(report.Namespaces), nil
}

func ValidateCompatibility(revision model.WorldRevision, build model.Build) error {
	if build.Minecraft == nil || revision.Minecraft != *build.Minecraft || !slices.Contains(revision.Requires.Loaders, build.Loader) {
		return model.Failure("build_mismatch", "world Minecraft version or loader is incompatible")
	}
	missing := []string{}
	for _, namespace := range revision.Requires.Namespaces {
		if !slices.Contains(build.ModIDs, namespace) {
			missing = append(missing, namespace)
		}
	}
	for _, id := range revision.Requires.Mods {
		if !slices.Contains(build.ModIDs, id) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return model.Failure("build_mismatch", "build is missing namespaces: "+strings.Join(slices.Compact(missing), ", "))
	}
	return nil
}

func restoring(asset model.World) bool {
	return asset.Restore != nil && (asset.Restore.Status == "queued" || asset.Restore.Status == "running")
}

func intact(filename, digest string, size int64) (bool, error) {
	actual, actualSize, err := worldarchive.Digest(filename)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return actual == digest && actualSize == size, nil
}

func downloadURL(project, id, revision string) string {
	return "/v2/projects/" + url.PathEscape(project) + "/worlds/" + url.PathEscape(id) + "/revisions/" + revision + "/download"
}

func (m *Manager) Packs(ctx context.Context) ([]model.PackRevision, error) {
	return store.All[model.PackRevision](ctx, m.db, "pack_revision")
}
