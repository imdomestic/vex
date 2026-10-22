package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/spf13/pflag"
)

func planCommand(ctx context.Context, global *pflag.FlagSet, args []string) (requestPlan, config.Resolved[config.Client], error) {
	plan := requestPlan{Method: http.MethodGet}
	var resolved config.Resolved[config.Client]
	if args[0] == "world" || args[0] == "pack" {
		return publicationCommand(ctx, global, args)
	}
	flags := commandFlags(global, args[0])
	var commit, loader, seed, world, branch, before, label *string
	var force *bool
	var limit *int
	switch args[0] {
	case "builds", "rooms":
		commit = flags.String("commit", "", "full commit SHA")
		loader = flags.String("loader", "", "loader ID")
		if args[0] == "builds" {
			force = flags.Bool("force", false, "create a new build attempt")
		} else {
			seed = flags.String("seed", "", "world seed")
			world = flags.String("world", "", "published world reference")
			label = flags.String("label", "", "room label")
		}
	case "git":
		branch = flags.String("branch", "", "branch name")
		before = flags.String("before", "", "pagination commit")
		limit = flags.Int("limit", 50, "maximum commits")
	case "health", "meta", "me", "nodes", "projects", "snapshots", "request":
	default:
		return plan, resolved, fmt.Errorf("unknown command")
	}
	if err := parse(flags, args[1:]); err != nil {
		return plan, resolved, err
	}
	var err error
	resolved, err = resolveClient(global)
	if err != nil {
		return plan, resolved, err
	}
	if err = selectClient(global, &resolved); err != nil {
		return plan, resolved, err
	}
	positionals := flags.Args()
	if args[0] == "request" {
		if len(positionals) < 2 || len(positionals) > 3 {
			return plan, resolved, fmt.Errorf("usage: request METHOD /v2/path [JSON-file]")
		}
		plan.Method, plan.Path = positionals[0], positionals[1]
		if len(positionals) == 3 {
			data, err := os.ReadFile(positionals[2])
			if err != nil {
				return plan, resolved, err
			}
			if !json.Valid(data) {
				return plan, resolved, fmt.Errorf("invalid JSON input")
			}
			plan.Body = json.RawMessage(data)
		}
		return plan, resolved, nil
	}
	if args[0] == "health" || args[0] == "meta" || args[0] == "me" || args[0] == "nodes" {
		if len(positionals) != 0 {
			return plan, resolved, fmt.Errorf("command accepts no positional arguments")
		}
		plan.Path = "/v2/" + args[0]
		return plan, resolved, nil
	}
	if args[0] == "projects" {
		plan.Path = "/v2/projects"
		if len(positionals) == 0 || (len(positionals) == 1 && positionals[0] == "list") {
			return plan, resolved, nil
		}
		if len(positionals) != 2 || positionals[0] != "show" || !config.ValidID(positionals[1]) {
			return plan, resolved, fmt.Errorf("usage: projects list | projects show ID")
		}
		plan.Path += "/" + positionals[1]
		return plan, resolved, nil
	}
	if err = selectProject(ctx, &resolved.Config); err != nil {
		return plan, resolved, err
	}
	prefix, err := projectPrefix(resolved.Config)
	if err != nil {
		return plan, resolved, err
	}
	if args[0] == "git" {
		if len(positionals) != 1 {
			return plan, resolved, fmt.Errorf("usage: git refresh|branches|commits")
		}
		switch positionals[0] {
		case "refresh":
			plan.Method, plan.Path = http.MethodPost, prefix+"/git/refresh"
		case "branches":
			plan.Path = prefix + "/branches"
		case "commits":
			if *limit <= 0 {
				return plan, resolved, fmt.Errorf("commit limit must be positive")
			}
			query := url.Values{"limit": {fmt.Sprint(*limit)}}
			if *branch != "" {
				query.Set("branch", *branch)
			}
			if *before != "" {
				query.Set("before", *before)
			}
			plan.Path = prefix + "/commits?" + query.Encode()
		default:
			return plan, resolved, fmt.Errorf("unknown Git operation")
		}
		return plan, resolved, nil
	}
	plan.Path = prefix + "/" + args[0]
	if len(positionals) == 0 || (len(positionals) == 1 && positionals[0] == "list") {
		return plan, resolved, nil
	}
	if args[0] == "builds" && positionals[0] == "create" {
		if len(positionals) != 1 || *commit == "" || *loader == "" {
			return plan, resolved, fmt.Errorf("builds create requires --commit SHA and --loader ID")
		}
		plan.Method = http.MethodPost
		plan.Body = map[string]any{"sha": *commit, "loader": *loader, "force": *force}
		return plan, resolved, nil
	}
	if args[0] == "rooms" && positionals[0] == "create" {
		if len(positionals) != 1 || *commit == "" || *loader == "" {
			return plan, resolved, fmt.Errorf("rooms create requires --commit SHA and --loader ID")
		}
		kind := "commit"
		if *world != "" {
			kind = "world"
		}
		plan.Method = http.MethodPost
		plan.Body = map[string]any{"kind": kind, "sha": *commit, "loader": *loader, "seed": *seed, "world": *world}
		return plan, resolved, nil
	}
	if args[0] == "builds" && len(positionals) <= 2 && positionals[0] != "show" && positionals[0] != "log" {
		plan.Path += "/" + url.PathEscape(positionals[0])
		if len(positionals) == 2 {
			if positionals[1] != "log" {
				return plan, resolved, fmt.Errorf("unknown builds operation")
			}
			plan.Path += "/log"
		}
		return plan, resolved, nil
	}
	if len(positionals) != 2 {
		return plan, resolved, fmt.Errorf("operation requires one object ID")
	}
	action, id := positionals[0], url.PathEscape(positionals[1])
	plan.Path += "/" + id
	switch action {
	case "show":
	case "log":
		if args[0] == "snapshots" {
			return plan, resolved, fmt.Errorf("snapshots have no log operation")
		}
		plan.Path += "/log"
	case "start", "stop", "restart":
		if args[0] != "rooms" {
			return plan, resolved, fmt.Errorf("operation requires a room")
		}
		plan.Method, plan.Path = http.MethodPost, plan.Path+"/"+action
		if action == "start" && flags.Changed("seed") {
			plan.Body = map[string]string{"seed": *seed}
		}
	case "reset-world":
		if args[0] != "rooms" {
			return plan, resolved, fmt.Errorf("reset-world requires a room")
		}
		body := map[string]string{}
		if flags.Changed("seed") {
			body["seed"] = *seed
		}
		plan.Method, plan.Path, plan.Body, plan.Confirm = http.MethodPost, plan.Path+"/reset-world", body, positionals[1]
	case "label":
		if args[0] != "rooms" || !flags.Changed("label") {
			return plan, resolved, fmt.Errorf("rooms label requires --label")
		}
		plan.Method, plan.Body = http.MethodPatch, map[string]string{"label": *label}
	case "delete":
		if args[0] != "rooms" {
			return plan, resolved, fmt.Errorf("delete requires a room")
		}
		plan.Method = http.MethodDelete
	case "download", "restore":
		if args[0] != "snapshots" {
			return plan, resolved, fmt.Errorf("operation requires a snapshot")
		}
		plan.Path += "/" + action
		if action == "restore" {
			plan.Method = http.MethodPost
		}
	default:
		return plan, resolved, fmt.Errorf("unknown object operation")
	}
	return plan, resolved, nil
}
