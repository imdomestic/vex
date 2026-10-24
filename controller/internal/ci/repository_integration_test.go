//go:build integration

package ci

import (
	"os"
	"strings"
	"testing"
)

// githubRepository 读取运维指定的 GitHub 测试仓库 owner/repository；测试不使用固定的项目仓库。
func githubRepository(t *testing.T, variable string) (string, string) {
	t.Helper()
	owner, repository, ok := strings.Cut(os.Getenv(variable), "/")
	if !ok || owner == "" || repository == "" || strings.Contains(repository, "/") {
		t.Fatalf("%s must name the dedicated GitHub test repository as owner/repository", variable)
	}
	return owner, repository
}
