package assets

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
	"unicode"

	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/imdomestic/chorus-testbed/controller/internal/worldarchive"
)

var assetID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var namespacePattern = regexp.MustCompile(`^[a-z0-9_.-]+$`)

type Manager struct {
	cfg config.Config
	db  *store.Store
}

func New(cfg config.Config, db *store.Store) (*Manager, error) {
	if !filepath.IsAbs(cfg.Server.DataDir) || db == nil {
		return nil, fmt.Errorf("assets require an absolute data directory and database")
	}
	for _, directory := range []string{"assets", "packs", ".work"} {
		if err := os.MkdirAll(filepath.Join(cfg.Server.DataDir, directory), 0700); err != nil {
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

func (m *Manager) List(ctx context.Context) ([]model.Asset, error) {
	values, err := store.All[model.Asset](ctx, m.db, "asset")
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

func (m *Manager) Get(ctx context.Context, id string) (model.Asset, error) {
	var value model.Asset
	if !assetID.MatchString(id) {
		return value, model.Failure("not_found", "asset does not exist")
	}
	err := m.db.Get(ctx, "asset", id, &value)
	return value, missing(err)
}

func validRevision(revision string) bool {
	number, err := strconv.ParseUint(revision, 10, 64)
	return err == nil && number > 0 && strconv.FormatUint(number, 10) == revision
}

func (m *Manager) Revision(ctx context.Context, id, revision string) (model.AssetRevision, error) {
	var value model.AssetRevision
	if !assetID.MatchString(id) || !validRevision(revision) {
		return value, model.Failure("not_found", "asset revision does not exist")
	}
	err := m.db.Get(ctx, "asset_revision", id+":"+revision, &value)
	return value, missing(err)
}

func validPackID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\:") {
		return false
	}
	for _, char := range id {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func (m *Manager) Pack(ctx context.Context, id, revision string) (model.PackRevision, error) {
	var value model.PackRevision
	if !validPackID(id) || !sha256Pattern.MatchString(revision) {
		return value, model.Failure("not_found", "pack revision does not exist")
	}
	err := m.db.Get(ctx, "pack_revision", id+":"+revision, &value)
	return value, missing(err)
}

func WorldPath(id, revision string) string { return path.Join("assets", id, revision, "world.tar.gz") }
func MetadataPath(id, revision string) string {
	return path.Join("assets", id, revision, "revision.json")
}
func PackPath(id, revision string) string         { return path.Join("packs", id, revision+".zip") }
func PackMetadataPath(id, revision string) string { return path.Join("packs", id, revision+".json") }

func (m *Manager) absolute(relative string) string {
	return filepath.Join(m.cfg.Server.DataDir, filepath.FromSlash(relative))
}

func (m *Manager) OpenWorld(ctx context.Context, id, revision string) (*os.File, error) {
	if !assetID.MatchString(id) || !validRevision(revision) {
		return nil, model.Failure("not_found", "asset revision does not exist")
	}
	var file *os.File
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		var value model.AssetRevision
		if err := tx.Get(ctx, "asset_revision", id+":"+revision, &value); err != nil {
			return missing(err)
		}
		var err error
		file, err = os.Open(m.absolute(WorldPath(id, revision)))
		if errors.Is(err, os.ErrNotExist) {
			return model.Failure("archive_failed", "asset revision requires restoration")
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
		file, err = os.Open(m.absolute(PackPath(id, revision)))
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

func (m *Manager) metadata(value model.AssetMetadata) (model.AssetMetadata, error) {
	if !assetID.MatchString(value.ID) || strings.TrimSpace(value.Title) == "" {
		return value, model.Failure("invalid_request", "asset ID or title is invalid")
	}
	if value.Minecraft != m.cfg.Minecraft.Version {
		return value, model.Failure("invalid_request", "asset Minecraft version differs from the controller")
	}
	if value.Source.Repo == "" || !commitPattern.MatchString(value.Source.Commit) || value.Source.Scale <= 0 || math.IsNaN(value.Source.Scale) || math.IsInf(value.Source.Scale, 0) {
		return value, model.Failure("invalid_request", "asset source requires repo, full commit SHA and positive scale")
	}
	if len(value.Requires.Loaders) == 0 {
		return value, model.Failure("invalid_request", "asset requires at least one loader")
	}
	value.Requires.Loaders = slices.Clone(value.Requires.Loaders)
	sort.Strings(value.Requires.Loaders)
	value.Requires.Loaders = slices.Compact(value.Requires.Loaders)
	for _, loader := range value.Requires.Loaders {
		if loader != "fabric" && loader != "neoforge" {
			return value, model.Failure("invalid_request", "asset loader is invalid")
		}
	}
	value.Requires.Namespaces = append([]string{}, value.Requires.Namespaces...)
	sort.Strings(value.Requires.Namespaces)
	value.Requires.Namespaces = slices.Compact(value.Requires.Namespaces)
	for _, namespace := range value.Requires.Namespaces {
		if namespace == "minecraft" || !namespacePattern.MatchString(namespace) {
			return value, model.Failure("invalid_request", "asset namespace is invalid")
		}
	}
	if value.ResourcePack != nil && (!validPackID(value.ResourcePack.ID) || !sha256Pattern.MatchString(value.ResourcePack.Rev)) {
		return value, model.Failure("invalid_request", "resource pack reference is invalid")
	}
	return value, nil
}

func (m *Manager) scan(ctx context.Context, source, minecraft string) ([]string, error) {
	if m.cfg.Assets.WorldScanner == "" {
		return nil, fmt.Errorf("assets.world_scanner is required for publishing")
	}
	command := exec.CommandContext(ctx, m.cfg.Assets.WorldScanner, "--archive", source, "--minecraft", minecraft, "--work-dir", m.absolute(".work"))
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

func ValidateCompatibility(revision model.AssetRevision, build model.Build) error {
	if build.Minecraft == nil || revision.Minecraft != *build.Minecraft || !slices.Contains(revision.Requires.Loaders, build.Loader) {
		return model.Failure("build_mismatch", "asset Minecraft version or loader is incompatible")
	}
	missing := []string{}
	for _, namespace := range revision.Requires.Namespaces {
		if !slices.Contains(build.ModIDs, namespace) {
			missing = append(missing, namespace)
		}
	}
	if revision.Requires.ChorusMod && !slices.Contains(build.ModIDs, "chorus") {
		missing = append(missing, "chorus")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return model.Failure("build_mismatch", "build is missing namespaces: "+strings.Join(slices.Compact(missing), ", "))
	}
	return nil
}

func restoring(asset model.Asset) bool {
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

func downloadURL(id, revision string) string {
	return "/v1/assets/" + url.PathEscape(id) + "/revisions/" + revision + "/download"
}
