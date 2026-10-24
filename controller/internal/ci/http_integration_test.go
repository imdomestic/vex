//go:build integration

package ci

import (
	"os"
	"strings"
	"testing"

	"github.com/google/go-github/v88/github"
)

func TestRealGitHubConditionalPullAndWorkflowLists(t *testing.T) {
	repository := os.Getenv("VEX_IT_PUBLIC_GITHUB_REPOSITORY")
	if repository == "" {
		t.Skip("requires an actual public GitHub repository with workflow runs")
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		t.Fatal("public repository must use owner/name")
	}
	base, err := github.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	client, transport, err := newConditionalClient(base)
	if err != nil {
		t.Fatal(err)
	}
	workflows, _, err := client.Actions.ListWorkflows(t.Context(), parts[0], parts[1], &github.ListOptions{PerPage: 1})
	if err != nil || len(workflows.Workflows) == 0 {
		t.Fatal("actual public repository has no readable workflow", apiFailure("list public workflows", err))
	}
	workflow := workflows.Workflows[0].GetID()
	for attempt := 0; attempt < 2; attempt++ {
		if _, _, err = client.PullRequests.List(t.Context(), parts[0], parts[1], &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}); err != nil {
			t.Fatal(apiFailure("read actual open pulls", err))
		}
		if _, _, err = client.Actions.ListWorkflowRunsByID(t.Context(), parts[0], parts[1], workflow, &github.ListWorkflowRunsOptions{Status: "success", ListOptions: github.ListOptions{PerPage: 100}}); err != nil {
			t.Fatal(apiFailure("read actual successful workflows", err))
		}
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.pages) != 2 || transport.notModified < 2 {
		t.Fatalf("actual GitHub did not return both conditional list responses: cached=%d notModified=%d", len(transport.pages), transport.notModified)
	}
	t.Logf("actual GitHub %s: separate open-pull and success-only workflow ETags, %d HTTP 304 responses decoded through the official SDK", repository, transport.notModified)
}
