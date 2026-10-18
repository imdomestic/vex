package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/githubapp"
)

func githubCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] != "check" {
		return fmt.Errorf("usage: vexd github check [--config FILE]")
	}
	resolved, err := config.LoadDaemon(config.Options{Args: args[1:]})
	if err != nil {
		return err
	}
	app, err := githubapp.New(resolved.Config.GitHub.App)
	if err != nil {
		return err
	}
	projects := map[string]string{}
	for name, project := range resolved.Config.Projects {
		if project.GitHub.Repository != "" {
			projects[name] = project.GitHub.Repository
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	report, checkErr := app.Check(ctx, projects)
	if err = json.NewEncoder(output).Encode(report); err != nil {
		return err
	}
	return checkErr
}

func githubIdentityCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: vexd user github-id LOGIN")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	login, id, err := githubapp.UserIdentity(ctx, args[0])
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Login    string `json:"login"`
		ID       int64  `json:"id"`
		Identity string `json:"identity"`
	}{login, id, fmt.Sprintf("github:%d", id)})
}
