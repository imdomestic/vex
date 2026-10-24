//go:build integration

package githubapp

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
)

func TestInstalledRepository(t *testing.T) {
	key := os.Getenv("VEX_IT_GITHUB_APP_KEY")
	if key == "" {
		t.Skip("VEX_IT_GITHUB_APP_KEY is required")
	}
	id, err := strconv.ParseInt(os.Getenv("VEX_IT_GITHUB_APP_ID"), 10, 64)
	if err != nil {
		t.Fatal("VEX_IT_GITHUB_APP_ID is invalid")
	}
	repository := os.Getenv("VEX_IT_GITHUB_REPOSITORY")
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		t.Fatal("VEX_IT_GITHUB_REPOSITORY is invalid")
	}
	app, err := New(config.GitHubApp{AppID: id, PrivateKeyFile: key})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	installations, _, err := app.client.Apps.ListInstallations(ctx, nil)
	if err != nil {
		t.Fatal(SafeError("list App installations", err))
	}
	for _, installation := range installations {
		t.Logf("installation %d account %s selection %s", installation.GetID(), installation.GetAccount().GetLogin(), installation.GetRepositorySelection())
	}
	client, err := app.Repository(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	repo, _, err := client.Repositories.Get(ctx, parts[0], parts[1])
	if err != nil {
		t.Fatal(SafeError("read repository", err))
	}
	if repo.GetFullName() != repository {
		t.Fatal("installation repository identity differs")
	}
	t.Logf("App %d, repository %s, default branch %s", app.ID, repo.GetFullName(), repo.GetDefaultBranch())
	report, err := app.Check(ctx, map[string]string{"integration": repository})
	if err != nil {
		t.Fatal(err)
	}
	if report.ID != id || report.Slug == "" || len(report.Installations) == 0 || len(report.Projects) != 1 || !report.Projects[0].Accessible {
		t.Fatal("App check did not return its installed repository")
	}
	for permission, access := range map[string]string{"actions": "read", "checks": "write", "contents": "read", "metadata": "read", "pull_requests": "write"} {
		if report.Projects[0].Permissions[permission] != access {
			t.Fatalf("unexpected installation grant for %s", permission)
		}
	}
}
