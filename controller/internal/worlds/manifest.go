package worlds

import (
	"fmt"
	"slices"

	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
)

type ManifestPolicy struct {
	SHA      string
	Document manifest.Document
	Commit   *model.CommitInfo
}

// snapshotPolicy 只使用快照记录本身保存的提交与清单。
func snapshotPolicy(snapshot model.Snapshot) (ManifestPolicy, error) {
	if snapshot.Manifest == nil || snapshot.WorldExtraRootEntries == nil {
		return ManifestPolicy{}, model.Failure("conflict", "快照没有记录提交清单，无法提升为世界")
	}
	return ManifestPolicy{SHA: snapshot.ManifestSHA, Document: *snapshot.Manifest, Commit: snapshot.Commit}, nil
}

func (p ManifestPolicy) Validate() error {
	if !commitPattern.MatchString(p.SHA) {
		return fmt.Errorf("world publication requires an authoritative full manifest commit")
	}
	return manifest.Validate(p.Document, manifest.Policy{})
}

func (p ManifestPolicy) MatchesSnapshot(snapshot model.Snapshot) bool {
	left, right := slices.Clone(p.Document.World.ExtraRootEntries), slices.Clone(snapshot.WorldExtraRootEntries)
	slices.Sort(left)
	slices.Sort(right)
	return p.SHA == snapshot.ManifestSHA && p.Document.Minecraft.Version == snapshot.Minecraft && slices.Equal(left, right)
}

func revisionPolicyRecorded(revision model.WorldRevision) error {
	if !commitPattern.MatchString(revision.ManifestSHA) || revision.ExtraRootEntries == nil {
		return fmt.Errorf("world revision requires authoritative manifest metadata initialization")
	}
	return nil
}
