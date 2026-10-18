package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
)

// ClientFingerprint 使用客户端文件摘要集合与版本信息生成跨平台稳定标识。
func ClientFingerprint(value model.Build) (string, error) {
	if value.Minecraft == nil || value.LoaderVersion == nil || !manifest.Version(*value.Minecraft) || !manifest.Version(*value.LoaderVersion) || (value.Loader != "fabric" && value.Loader != "neoforge") {
		return "", fmt.Errorf("client fingerprint requires complete Minecraft and loader versions")
	}
	canonical := struct {
		Minecraft     string   `json:"minecraft"`
		Loader        string   `json:"loader"`
		LoaderVersion string   `json:"loaderVersion"`
		SHA512        []string `json:"sha512"`
	}{*value.Minecraft, value.Loader, *value.LoaderVersion, []string{}}
	seen := map[string]bool{}
	for _, file := range value.Files {
		if !manifest.Side(file.Side) || !manifest.Digest(file.SHA512, 128) {
			return "", fmt.Errorf("client fingerprint requires valid file sides and SHA-512 digests")
		}
		if file.Side != "server" && !seen[file.SHA512] {
			seen[file.SHA512] = true
			canonical.SHA512 = append(canonical.SHA512, file.SHA512)
		}
	}
	sort.Strings(canonical.SHA512)
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
