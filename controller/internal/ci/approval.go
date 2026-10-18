package ci

import (
	"context"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/imdomestic/vex/controller/internal/model"
)

func writePermission(value string) bool {
	return value == "admin" || value == "write" || value == "maintain"
}

func (m *Manager) approve(ctx context.Context, client *github.Client, owner, repo string, pull model.Pull, notBefore time.Time) (bool, error) {
	permission := func(login string) (bool, error) {
		if login == "" {
			return false, nil
		}
		result, _, err := client.Repositories.GetPermissionLevel(ctx, owner, repo, login)
		if err != nil {
			return false, apiFailure("verify preview maintainer", err)
		}
		return writePermission(result.GetPermission()), nil
	}
	for comment, err := range client.Issues.ListCommentsIter(ctx, owner, repo, pull.Number, &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}) {
		if err != nil {
			return false, apiFailure("read preview approvals", err)
		}
		if strings.TrimSpace(comment.GetBody()) != "/vex preview" || comment.GetCreatedAt().Before(notBefore) {
			continue
		}
		allowed, err := permission(comment.GetUser().GetLogin())
		if err != nil || allowed {
			return allowed, err
		}
	}
	var latest *github.IssueEvent
	for event, err := range client.Issues.ListIssueEventsIter(ctx, owner, repo, pull.Number, &github.ListOptions{PerPage: 100}) {
		if err != nil {
			return false, apiFailure("read preview label approvals", err)
		}
		if event.GetLabel().GetName() != "vex-preview" || event.GetEvent() != "labeled" && event.GetEvent() != "unlabeled" {
			continue
		}
		if latest == nil || event.GetCreatedAt().After(latest.GetCreatedAt().Time) || event.GetCreatedAt().Equal(latest.GetCreatedAt()) && event.GetID() > latest.GetID() {
			latest = event
		}
	}
	if latest == nil || latest.GetEvent() != "labeled" || latest.GetCreatedAt().Before(notBefore) {
		return false, nil
	}
	return permission(latest.GetActor().GetLogin())
}
