package rooms

import (
	"context"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

// DefaultWorldManifest 读取默认分支当前提交的清单与元数据，供上传世界时写入 revision。
func (m *Manager) DefaultWorldManifest(ctx context.Context) (worlds.ManifestPolicy, error) {
	refs, err := m.Git.Branches(ctx)
	if err != nil {
		return worlds.ManifestPolicy{}, err
	}
	for _, ref := range refs {
		if ref.Name == m.Cfg.Git.DefaultBranch {
			document, err := m.Builds.Manifest(ctx, ref.Head)
			if err != nil {
				return worlds.ManifestPolicy{}, err
			}
			commit, err := m.Git.CommitInfo(ctx, ref.Head)
			if err != nil {
				return worlds.ManifestPolicy{}, err
			}
			return worlds.ManifestPolicy{SHA: ref.Head, Document: document, Commit: &commit}, nil
		}
	}
	return worlds.ManifestPolicy{}, model.Failure("conflict", "默认分支的提交清单尚未同步")
}
