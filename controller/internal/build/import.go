package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

type CIImport struct {
	SHA            string
	RunID          int64
	RunAttempt     int
	Repository     string
	HeadRepository string
	Workflow       string
	Event          string
	HTMLURL        string
	PRNumber       int
	BundlePath     string
}

type ciImportRecord struct {
	SHA            string        `json:"sha"`
	Repository     string        `json:"repository"`
	HeadRepository string        `json:"headRepository"`
	Workflow       string        `json:"workflow"`
	Event          string        `json:"event"`
	HTMLURL        string        `json:"htmlUrl"`
	Builds         []model.Build `json:"builds"`
}

// Manifest 返回指定提交经过项目策略校验的构建清单。
func (m *Manager) Manifest(ctx context.Context, sha string) (manifest.Document, error) {
	return m.commitManifest(ctx, sha)
}

func (m *Manager) commitManifest(ctx context.Context, sha string) (manifest.Document, error) {
	doc, _, err := manifest.ReadCommit(ctx, m.mirror, sha, m.cfg.ManifestFallback)
	if err != nil {
		return doc, err
	}
	policy := manifest.Policy{
		Minecraft: m.cfg.Minecraft.Version, Java: map[int]bool{}, Loaders: map[string]bool{},
		MaxMemory: m.cfg.Quota.RoomMemoryMax,
	}
	for key := range m.cfg.Runtimes {
		version, err := strconv.Atoi(strings.TrimPrefix(key, "java"))
		if err != nil {
			return doc, fmt.Errorf("invalid configured Java runtime key")
		}
		if m.cfg.Minecraft.Java == 0 || version == m.cfg.Minecraft.Java {
			policy.Java[version] = true
		}
	}
	for name, loader := range m.cfg.Loaders {
		policy.Loaders[name] = loader.Enabled
	}
	return doc, manifest.Validate(doc, policy)
}

// ImportCI 的身份字段由 GitHub 权威 API 调用方提供，ZIP 只提供产物内容。
func (m *Manager) ImportCI(ctx context.Context, input CIImport) ([]model.Build, bool, error) {
	if m.cfg.Build.Source != "ci" || input.RunID <= 0 || input.RunAttempt < 1 || input.PRNumber < 0 || input.Repository == "" || input.Repository != m.cfg.GitHub.Repository || input.Workflow == "" || input.Workflow != m.cfg.CI.Workflow {
		return nil, false, model.Failure("invalid_request", "CI import identity differs from configured project")
	}
	doc, err := m.commitManifest(ctx, input.SHA)
	if err != nil {
		return nil, false, err
	}
	stage, err := os.MkdirTemp(filepath.Join(m.cfg.DataDir, ".work"), "ci-import-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(stage)
	extracted := filepath.Join(stage, "contents")
	bundle, err := UnpackBundle(input.BundlePath, extracted)
	if err != nil {
		return nil, false, err
	}
	if err = bundle.validateManifest(doc, true); err != nil {
		return nil, false, err
	}
	result := []model.Build{}
	created := false
	key := fmt.Sprintf("%d-%d", input.RunID, input.RunAttempt)
	err = m.db.Update(ctx, func(tx *store.Tx) error {
		var imported ciImportRecord
		readErr := tx.Get(ctx, "ci_import", key, &imported)
		if readErr == nil {
			if imported.SHA != input.SHA || imported.Repository != input.Repository || imported.Workflow != input.Workflow || imported.HeadRepository != input.HeadRepository || imported.Event != input.Event || imported.HTMLURL != input.HTMLURL {
				return model.Failure("conflict", "CI run identity changed")
			}
			result = imported.Builds
			return nil
		}
		if !errors.Is(readErr, store.ErrNotFound) {
			return readErr
		}
		current := make([]model.Build, 0, len(bundle.Loaders))
		stale := false
		for _, selected := range bundle.LoaderBuilds() {
			var previous model.Build
			err := tx.Get(ctx, "build", selected.Loader+"-"+input.SHA[:12], &previous)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err == nil {
				current = append(current, previous)
				if previous.Source.Kind == "ci" && (previous.Source.RunID > input.RunID || (previous.Source.RunID == input.RunID && previous.Source.RunAttempt > input.RunAttempt)) {
					stale = true
				}
			}
		}
		if stale {
			result = current
			return nil
		}
		rooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		for _, selected := range bundle.LoaderBuilds() {
			id := selected.Loader + "-" + input.SHA[:12]
			var previous model.Build
			referenced := false
			err = tx.Get(ctx, "build", id, &previous)
			if err == nil {
				if previous.SHA != input.SHA || previous.Status == "building" || (previous.Status == "queued" && previous.Source.Kind != "ci") {
					return model.Failure("conflict", "build already exists with an incompatible identity or active worker")
				}
				for _, room := range rooms {
					if previous.Status == "ready" && room.BuildID == id && room.State != "closed" {
						referenced = true
					}
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if referenced {
				identical, err := m.identicalPublication(previous, doc, selected)
				if err != nil {
					return err
				}
				if !identical {
					return model.Failure("conflict", "different CI contents cannot replace a build referenced by a registered room")
				}
				result = append(result, previous)
				continue
			}
			attempt := previous.Attempt
			if previous.Status != "queued" || previous.Source.Kind != "ci" {
				attempt, err = tx.Next(ctx, "build_attempt:"+id)
				if err != nil {
					return err
				}
			}
			now, duration := time.Now().UTC(), 0.0
			value := model.Build{
				Project: m.cfg.ID, ID: id, SHA: input.SHA, Loader: selected.Loader, Status: "ready", Attempt: attempt,
				Source:   model.BuildSource{Kind: "ci", Repository: input.Repository, Workflow: input.Workflow, RunID: input.RunID, RunAttempt: input.RunAttempt, PRNumber: input.PRNumber, Event: input.Event, HTMLURL: input.HTMLURL},
				QueuedAt: now, StartedAt: &now, FinishedAt: &now, BuiltAt: &now, DurationSec: &duration,
				Log: m.apiPrefix() + "/builds/" + id + "/log",
			}
			if err = m.publishFiles(&value, doc, selected, extracted); err != nil {
				return err
			}
			if err = tx.Put(ctx, "build", id, value); err != nil {
				return err
			}
			if err = tx.Put(ctx, "build_access", id, accessRecord{At: now}); err != nil {
				return err
			}
			result = append(result, value)
			created = true
		}
		return tx.Put(ctx, "ci_import", key, ciImportRecord{SHA: input.SHA, Repository: input.Repository, HeadRepository: input.HeadRepository, Workflow: input.Workflow, Event: input.Event, HTMLURL: input.HTMLURL, Builds: result})
	})
	return result, created, err
}

func (m *Manager) identicalPublication(current model.Build, doc manifest.Document, selected BundleBuild) (bool, error) {
	if current.Minecraft == nil || current.LoaderVersion == nil || current.Java == nil || *current.Minecraft != doc.Minecraft.Version || *current.LoaderVersion != doc.Loaders[selected.Loader].LoaderVersion || *current.Java != doc.Minecraft.Java || current.RoomMemory != doc.Rooms.Memory {
		return false, nil
	}
	before, after := append([]string{}, current.WorldExtraRootEntries...), append([]string{}, doc.World.ExtraRootEntries...)
	sort.Strings(before)
	sort.Strings(after)
	if strings.Join(before, "\x00") != strings.Join(after, "\x00") {
		return false, nil
	}
	expected := map[string]model.BuildFile{}
	for _, file := range selected.Files {
		expected[file.Name] = model.BuildFile{Name: file.Name, Side: file.Side, Size: file.Size, SHA1: file.SHA1, SHA256: file.SHA256, SHA512: file.SHA512}
	}
	if doc.Loaders[selected.Loader].Agent {
		agent, _, err := m.agentArtifact(current)
		if err != nil {
			return false, err
		}
		if agent == nil {
			return false, fmt.Errorf("manifest requests an unavailable platform agent")
		}
		if _, exists := expected[agent.Name]; exists {
			return false, fmt.Errorf("platform agent filename conflicts with a bundle file")
		}
		expected[agent.Name] = *agent
	}
	if len(current.Files) != len(expected) {
		return false, nil
	}
	for _, file := range current.Files {
		other, exists := expected[file.Name]
		if !exists || file.Size != other.Size || file.Side != other.Side || file.SHA1 != other.SHA1 || file.SHA256 != other.SHA256 || file.SHA512 != other.SHA512 {
			return false, nil
		}
	}
	return true, nil
}

func (m *Manager) publishFiles(value *model.Build, doc manifest.Document, selected BundleBuild, extracted string) error {
	minecraft, java, loaderVersion := doc.Minecraft.Version, doc.Minecraft.Java, doc.Loaders[value.Loader].LoaderVersion
	value.Minecraft, value.Java, value.LoaderVersion = &minecraft, &java, &loaderVersion
	value.WorldExtraRootEntries = append([]string{}, doc.World.ExtraRootEntries...)
	value.RoomMemory = doc.Rooms.Memory
	value.Files = []model.BuildFile{}
	outputDir := m.attemptDir(*value)
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return err
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, file := range selected.Files {
		target := filepath.Join(outputDir, file.Name)
		if err := copyImmutable(filepath.Join(extracted, filepath.FromSlash(file.Path)), target); err != nil {
			return err
		}
		entry, err := m.buildFile(target, *value, file.Side)
		if err != nil {
			return err
		}
		if entry.Size != file.Size || entry.SHA1 != file.SHA1 || entry.SHA256 != file.SHA256 || entry.SHA512 != file.SHA512 {
			return fmt.Errorf("bundle file changed during publication")
		}
		entry.Origin, entry.SourceURL = file.Origin, file.SourceURL
		value.Files = append(value.Files, entry)
		names[strings.ToLower(file.Name)] = true
		found, err := JarModIDs(target, value.Loader)
		if err != nil {
			return err
		}
		for _, id := range found {
			if id == "vex_agent" {
				return fmt.Errorf("project bundle cannot supply the platform agent")
			}
			ids[id] = true
		}
	}
	if doc.Loaders[value.Loader].Agent {
		agent, source, err := m.agentArtifact(*value)
		if err != nil {
			return err
		}
		if agent == nil {
			return fmt.Errorf("manifest requests an agent but no compatible platform agent is configured")
		}
		if names[strings.ToLower(agent.Name)] {
			return fmt.Errorf("platform agent filename conflicts with a bundle file")
		}
		target := filepath.Join(outputDir, agent.Name)
		if err = copyImmutable(source, target); err != nil {
			return err
		}
		copied, err := m.buildFile(target, *value, "server")
		if err != nil {
			return err
		}
		if copied.SHA512 != agent.SHA512 || copied.Size != agent.Size {
			return fmt.Errorf("platform agent changed during publication")
		}
		copied.Origin = "agent"
		value.Files = append(value.Files, copied)
		ids["vex_agent"] = true
	}
	value.ModIDs = make([]string, 0, len(ids))
	for id := range ids {
		value.ModIDs = append(value.ModIDs, id)
	}
	sort.Strings(value.ModIDs)
	fingerprint, err := ClientFingerprint(*value)
	if err != nil {
		return err
	}
	value.ClientFingerprint = fingerprint
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(outputDir, "manifest.json"), append(data, '\n'), 0640); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(outputDir, "build.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	return log.Close()
}

func copyImmutable(source, target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return copyFile(source, target)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return fmt.Errorf("existing publication file is not a bounded regular file")
	}
	expected, err := describeFile(source, "")
	if err != nil {
		return err
	}
	actual, err := describeFile(target, "")
	if err != nil {
		return err
	}
	if actual.SHA512 != expected.SHA512 || actual.Size != expected.Size {
		return fmt.Errorf("existing publication file has different contents")
	}
	return nil
}
