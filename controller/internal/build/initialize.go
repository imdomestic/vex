package build

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

// Backfill 从镜像补录已有构建缺少的提交元数据与清单，保留发布文件的实际内容。
// 提交或清单已经无法读取的记录写入返回的列表，不影响其他记录；记录之间不一致时返回错误。
func (m *Manager) Backfill(ctx context.Context) ([]model.RecordIssue, error) {
	values, err := store.All[model.Build](ctx, m.db, "build")
	if err != nil {
		return nil, err
	}
	issues := []model.RecordIssue{}
	for _, value := range values {
		issue, err := m.backfillRecord(ctx, value)
		if err != nil {
			return nil, fmt.Errorf("backfill build %s: %w", value.ID, err)
		}
		if issue != "" {
			issues = append(issues, model.RecordIssue{Kind: "build", ID: value.ID, SHA: value.SHA, Reason: issue})
		}
	}
	return issues, nil
}

// RecordIssue 区分记录自身无法补录的原因与需要立即失败的错误。
func RecordIssue(ctx context.Context, err error) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var failure *model.Error
	if errors.As(err, &failure) && failure.Code == "commit_unavailable" {
		return "commit_unavailable", nil
	}
	return "manifest_unavailable: " + err.Error(), nil
}

func (m *Manager) backfillRecord(ctx context.Context, value model.Build) (string, error) {
	needCommit := value.Commit == nil
	needManifest := value.Status == "ready" && (value.Manifest == nil || value.ClientFingerprint == "")
	if !needCommit && !needManifest {
		if value.Status == "ready" {
			return "", ValidateMetadata(value)
		}
		return "", nil
	}
	var commit *model.CommitInfo
	if needCommit {
		info, err := m.mirror.CommitInfo(ctx, value.SHA)
		if err != nil {
			return RecordIssue(ctx, err)
		}
		commit = &info
	}
	var document *manifest.Document
	if needManifest {
		doc, err := m.Manifest(ctx, value.SHA)
		if err != nil {
			return RecordIssue(ctx, err)
		}
		loader, exists := doc.Loaders[value.Loader]
		if !exists || value.Minecraft == nil || value.Java == nil || value.LoaderVersion == nil || *value.Minecraft != doc.Minecraft.Version || *value.Java != doc.Minecraft.Java || *value.LoaderVersion != loader.LoaderVersion {
			return "manifest_differs: target differs from its commit manifest", nil
		}
		if value.ClientFingerprint != "" && (value.RoomMemory != doc.Rooms.Memory || !slices.Equal(value.WorldExtraRootEntries, doc.World.ExtraRootEntries)) {
			return "manifest_differs: world or room policy differs from its commit manifest", nil
		}
		document = &doc
	}
	var updated model.Build
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Get(ctx, "build", value.ID, &updated); err != nil {
			return err
		}
		if updated.Attempt != value.Attempt || updated.SHA != value.SHA || updated.Status != value.Status {
			return model.Failure("conflict", "build changed while backfilling its records")
		}
		if commit != nil && updated.Commit == nil {
			updated.Commit = commit
		}
		if document != nil {
			if updated.ClientFingerprint == "" {
				fingerprint, err := ClientFingerprint(updated)
				if err != nil {
					return err
				}
				updated.WorldExtraRootEntries = append([]string{}, document.World.ExtraRootEntries...)
				updated.RoomMemory = document.Rooms.Memory
				updated.ClientFingerprint = fingerprint
			}
			if updated.Manifest == nil {
				updated.Manifest = document
			}
		}
		return tx.Put(ctx, "build", updated.ID, updated)
	})
	if err != nil {
		return "", err
	}
	if updated.Status == "ready" {
		return "", ValidateMetadata(updated)
	}
	return "", nil
}

// ValidateMetadata 检查启动房间所需的持久化清单信息，不访问 Git 镜像。
func ValidateMetadata(value model.Build) error {
	if value.ClientFingerprint == "" || value.WorldExtraRootEntries == nil {
		return model.Failure("conflict", "build manifest metadata is awaiting initialization")
	}
	fingerprint, err := ClientFingerprint(value)
	if err != nil {
		return err
	}
	if fingerprint != value.ClientFingerprint || value.Java == nil || *value.Java < 8 || *value.Java > 99 {
		return fmt.Errorf("build manifest metadata is inconsistent")
	}
	if err := manifest.ValidateRootEntries(value.WorldExtraRootEntries, nil); err != nil {
		return err
	}
	return manifest.ValidateMemory(value.RoomMemory, "")
}
