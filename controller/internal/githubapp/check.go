package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/go-github/v88/github"
)

type CheckReport struct {
	ID            int64                `json:"id"`
	Slug          string               `json:"slug"`
	Owner         string               `json:"owner"`
	Installations []InstallationReport `json:"installations"`
	Projects      []RepositoryReport   `json:"projects"`
}

type InstallationReport struct {
	ID          int64             `json:"id"`
	Account     string            `json:"account"`
	Selection   string            `json:"selection"`
	Permissions map[string]string `json:"permissions"`
}

type RepositoryReport struct {
	Project       string            `json:"project"`
	Repository    string            `json:"repository"`
	Accessible    bool              `json:"accessible"`
	DefaultBranch string            `json:"defaultBranch,omitempty"`
	Permissions   map[string]string `json:"permissions,omitempty"`
	Error         string            `json:"error,omitempty"`
}

func permissions(value any) (map[string]string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]string
	err = json.Unmarshal(data, &result)
	return result, err
}

func (a *App) Check(ctx context.Context, projects map[string]string) (CheckReport, error) {
	report := CheckReport{Installations: []InstallationReport{}, Projects: []RepositoryReport{}}
	app, _, err := a.client.Apps.Get(ctx, "")
	if err != nil {
		return report, SafeError("read App identity", err)
	}
	report.ID, report.Slug, report.Owner = app.GetID(), app.GetSlug(), app.GetOwner().GetLogin()
	options := &github.ListOptions{PerPage: 100}
	for {
		installations, response, err := a.client.Apps.ListInstallations(ctx, options)
		if err != nil {
			return report, SafeError("list installations", err)
		}
		for _, installation := range installations {
			grants, err := permissions(installation.Permissions)
			if err != nil {
				return report, err
			}
			report.Installations = append(report.Installations, InstallationReport{
				ID: installation.GetID(), Account: installation.GetAccount().GetLogin(),
				Selection: installation.GetRepositorySelection(), Permissions: grants,
			})
		}
		if response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)
	var failures []error
	for _, name := range names {
		result := RepositoryReport{Project: name, Repository: projects[name]}
		err := a.checkRepository(ctx, &result)
		if err != nil {
			result.Error = err.Error()
			failures = append(failures, fmt.Errorf("project %s: %w", name, err))
		}
		report.Projects = append(report.Projects, result)
	}
	return report, errors.Join(failures...)
}

func (a *App) checkRepository(ctx context.Context, report *RepositoryReport) error {
	client, err := a.Repository(ctx, report.Repository)
	if err != nil {
		return err
	}
	parts := strings.Split(report.Repository, "/")
	repository, _, err := client.Repositories.Get(ctx, parts[0], parts[1])
	if err != nil {
		return SafeError("read repository", err)
	}
	a.mu.Lock()
	transport := a.tokens[report.Repository]
	a.mu.Unlock()
	grants, err := transport.Permissions()
	if err != nil {
		return fmt.Errorf("cannot read installation token permissions")
	}
	report.Permissions, err = permissions(grants)
	if err != nil {
		return err
	}
	report.Accessible, report.DefaultBranch = true, repository.GetDefaultBranch()
	return nil
}

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func UserIdentity(ctx context.Context, login string) (string, int64, error) {
	if !loginPattern.MatchString(login) || strings.HasSuffix(login, "-") || strings.Contains(login, "--") {
		return "", 0, fmt.Errorf("invalid GitHub login")
	}
	client, err := github.NewClient()
	if err != nil {
		return "", 0, err
	}
	user, _, err := client.Users.Get(ctx, login)
	if err != nil {
		return "", 0, SafeError("read user identity", err)
	}
	if user.GetID() <= 0 {
		return "", 0, fmt.Errorf("GitHub returned an invalid user identity")
	}
	return user.GetLogin(), user.GetID(), nil
}
