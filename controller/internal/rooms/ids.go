package rooms

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var assetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)
var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

func ValidSHA(sha string) bool          { return shaPattern.MatchString(sha) }
func ValidAsset(id string) bool         { return assetPattern.MatchString(id) }
func BuildID(sha, loader string) string { return loader + "-" + sha[:12] }

func BranchID(branch, loader string, collision bool) string {
	slug := strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(branch), "-"), "-")
	hash := sha256.Sum256([]byte(branch))
	suffix := "-" + hex.EncodeToString(hash[:2])
	if slug == "" {
		slug = "branch"
		collision = true
	}
	max := 40 - len(loader) - 1
	if collision || len(slug) > max {
		if len(slug) > max-len(suffix) {
			slug = strings.TrimRight(slug[:max-len(suffix)], "-")
		}
		slug += suffix
	}
	return slug + "-" + loader
}

func RoomID(kind, sha, loader, asset string) (string, error) {
	if !ValidSHA(sha) || loader != "fabric" {
		return "", model.Failure("invalid_request", "完整提交 SHA 或 loader 无效")
	}
	switch kind {
	case "commit":
		return "c" + sha[:8] + "-" + loader, nil
	case "asset":
		if !ValidAsset(asset) {
			return "", model.Failure("invalid_request", "资产 ID 无效")
		}
		return "a-" + asset + "-" + sha[:8] + "-" + loader, nil
	default:
		return "", model.Failure("invalid_request", "房间种类必须为 commit 或 asset")
	}
}

func Active(state string) bool {
	return state == "queued" || state == "building" || state == "starting" || state == "ready"
}

func CanStart(room model.Room) error {
	if room.Label == "finished" {
		return model.Failure("room_finished", "房间已经标记为结束")
	}
	switch room.State {
	case "stopping", "archiving":
		return model.Failure("conflict", "房间正在停止或归档")
	case "lost":
		return model.Failure("node_lost", "房间所在节点已经失联")
	case "stopped", "closed", "failed", "queued", "building", "starting", "ready":
		return nil
	default:
		return model.Failure("conflict", "房间当前状态无法启动")
	}
}
