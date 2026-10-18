package build

import (
	"context"
	"errors"
	"fmt"

	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

// InitializeRecords 补录已有构建的提交清单信息，保留发布文件的实际内容。
func (m *Manager) InitializeRecords(ctx context.Context) error {
	values, err := store.All[model.Build](ctx, m.db, "build")
	if err != nil {
		return err
	}
	var failures []error
	for _, value := range values {
		if value.Status != "ready" {
			continue
		}
		if err := m.initializeRecord(ctx, value); err != nil {
			failures = append(failures, fmt.Errorf("initialize build %s: %w", value.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) initializeRecord(ctx context.Context, value model.Build) error {
	if value.ClientFingerprint != "" {
		return ValidateMetadata(value)
	}
	doc, err := m.Manifest(ctx, value.SHA)
	if err != nil {
		return fmt.Errorf("read commit manifest: %w", err)
	}
	loader, exists := doc.Loaders[value.Loader]
	if !exists || value.Minecraft == nil || value.Java == nil || value.LoaderVersion == nil || *value.Minecraft != doc.Minecraft.Version || *value.Java != doc.Minecraft.Java || *value.LoaderVersion != loader.LoaderVersion {
		return fmt.Errorf("target differs from its commit manifest")
	}
	fingerprint, err := ClientFingerprint(value)
	if err != nil {
		return err
	}
	return m.db.Update(ctx, func(tx *store.Tx) error {
		var current model.Build
		if err := tx.Get(ctx, "build", value.ID, &current); err != nil {
			return err
		}
		if current.Attempt != value.Attempt || current.SHA != value.SHA || current.Status != "ready" {
			return model.Failure("conflict", "build changed while initializing manifest metadata")
		}
		if current.ClientFingerprint != "" {
			return model.Failure("conflict", "build metadata changed while initializing")
		}
		current.WorldExtraRootEntries = append([]string{}, doc.World.ExtraRootEntries...)
		current.RoomMemory = doc.Rooms.Memory
		current.ClientFingerprint = fingerprint
		return tx.Put(ctx, "build", current.ID, current)
	})
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
