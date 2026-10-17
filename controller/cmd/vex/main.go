package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/spf13/pflag"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type client struct {
	cfg   config.Client
	token string
	http  *http.Client
}

type requestPlan struct {
	Method string
	Path   string
	Body   any
	Upload *streamUpload
}

func globals(args ...string) *pflag.FlagSet {
	flags := pflag.NewFlagSet("vex", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.String("config", "", "configuration file")
	flags.String("url", "", "API URL")
	flags.String("token-file", "", "bearer token file")
	flags.StringP("project", "p", "", "project ID")
	flags.String("timeout", "", "request timeout")
	flags.String("server", "", "configured server ID")
	flags.String("default-server", "", "default server ID")
	for _, arg := range args {
		name, _, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if strings.HasPrefix(arg, "--servers.") && flags.Lookup(name) == nil {
			flags.String(name, "", "server configuration value")
		}
	}
	return flags
}

func commandFlags(global *pflag.FlagSet, name string) *pflag.FlagSet {
	flags := pflag.NewFlagSet(name, pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.AddFlagSet(global)
	return flags
}

func parse(flags *pflag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return err
		}
		return fmt.Errorf("invalid %s arguments", flags.Name())
	}
	return nil
}

func resolveClient(global *pflag.FlagSet) (config.Resolved[config.Client], error) {
	args := []string{}
	global.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			args = append(args, "--"+flag.Name+"="+flag.Value.String())
		}
	})
	return config.LoadClient(config.Options{Args: args})
}

const usage = "usage: vex [--config FILE] [--server NAME] [-p PROJECT] login|logout|whoami|user|auth|config|health|meta|me|projects|nodes|git|builds|rooms|snapshots|world|pack|request"

func run(ctx context.Context, args []string, output io.Writer) error {
	return runWithInput(ctx, args, os.Stdin, output)
}

func runWithInput(ctx context.Context, args []string, input io.Reader, output io.Writer) (resultErr error) {
	defer func() {
		if errors.Is(resultErr, pflag.ErrHelp) {
			_, resultErr = fmt.Fprintln(output, usage)
		}
	}()
	global := globals(args...)
	global.SetInterspersed(false)
	if err := parse(global, args); err != nil {
		return err
	}
	args = global.Args()
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "login" || args[0] == "logout" || args[0] == "whoami" || args[0] == "auth" || args[0] == "user" {
		return loginCommand(ctx, global, args, input, output)
	}
	if args[0] == "config" {
		if len(args) < 2 || (args[1] != "show" && args[1] != "check") {
			return fmt.Errorf("usage: vex config show [--sources] | config check")
		}
		flags := commandFlags(global, "config")
		sources := flags.Bool("sources", false, "show configuration sources")
		if err := parse(flags, args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("config accepts no positional arguments")
		}
		resolved, err := resolveClient(global)
		if err != nil {
			return err
		}
		if args[1] == "check" {
			_, err = fmt.Fprintln(output, "configuration valid")
			return err
		}
		data, err := resolved.Render(*sources)
		if err != nil {
			return err
		}
		_, err = output.Write(data)
		return err
	}
	plan, resolved, err := planCommand(ctx, global, args)
	if err != nil {
		return err
	}
	if plan.Upload != nil {
		defer func() { resultErr = errors.Join(resultErr, plan.Upload.Close()) }()
	}
	c, err := newClient(resolved.Config, args[0] == "health" || args[0] == "meta")
	if err != nil {
		return err
	}
	response, err := c.do(ctx, plan)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if plan.Upload != nil {
		data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		if err != nil {
			return err
		}
		if err = plan.Upload.Verify(data); err != nil {
			return err
		}
		_, err = output.Write(data)
		return err
	}
	_, err = io.Copy(output, response.Body)
	return err
}

func newClient(cfg config.Client, public bool) (*client, error) {
	token := ""
	if cfg.TokenFile != "" {
		data, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read token file")
		}
		token = strings.TrimSpace(string(data))
	} else if cfg.Path != "" {
		var err error
		token, err = credentialStore(cfg).Get(cfg.URL)
		if err != nil && !errors.Is(err, auth.ErrUnauthenticated) {
			return nil, err
		}
	}
	if !public && token == "" {
		return nil, fmt.Errorf("login is required for this server")
	}
	if strings.ContainsAny(token, "\r\n\x00") || len(token) > 512 {
		return nil, fmt.Errorf("invalid local credential")
	}
	return &client{cfg: cfg, token: token, http: &http.Client{
		Timeout:       config.Duration(cfg.Timeout),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second},
	}}, nil
}

func credentialStore(cfg config.Client) auth.CredentialStore {
	return auth.CredentialStore{Directory: filepath.Join(filepath.Dir(cfg.Path), "credentials")}
}

func selectClient(global *pflag.FlagSet, resolved *config.Resolved[config.Client]) error {
	if origin := resolved.Sources["url"]; origin.Layer == "command" || origin.Layer == "environment" {
		if !global.Changed("server") {
			resolved.Config.Server, resolved.Config.DefaultServer = "", ""
			resolved.Config.Servers = nil
		}
	}
	var err error
	resolved.Config, err = resolved.Config.Selected()
	return err
}

func selectProject(ctx context.Context, cfg *config.Client) error {
	if cfg.Project != "" {
		return nil
	}
	c, err := newClient(*cfg, false)
	if err != nil {
		return err
	}
	var projects []struct {
		ID string `json:"id"`
	}
	if err = c.get(ctx, "/v2/projects", &projects); err != nil {
		return err
	}
	if len(projects) == 1 {
		cfg.Project = projects[0].ID
		return nil
	}
	names := make([]string, 0, len(projects))
	for _, project := range projects {
		names = append(names, project.ID)
	}
	return fmt.Errorf("select a project using -p; accessible projects: %s", strings.Join(names, ", "))
}

func (c *client) do(ctx context.Context, plan requestPlan) (*http.Response, error) {
	if !strings.HasPrefix(plan.Path, "/v2/") || strings.HasPrefix(plan.Path, "//") {
		return nil, fmt.Errorf("request path must use /v2/")
	}
	var reader io.Reader
	if plan.Body != nil {
		data, err := json.Marshal(plan.Body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	if plan.Upload != nil {
		reader = plan.Upload.Reader
	}
	req, err := http.NewRequestWithContext(ctx, plan.Method, strings.TrimRight(c.cfg.URL, "/")+plan.Path, reader)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if plan.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if plan.Upload != nil {
		req.Header.Set("Content-Type", plan.Upload.ContentType)
		req.ContentLength = plan.Upload.ContentLength
	}
	response, err := c.http.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			return nil, fmt.Errorf("API request failed: %w", requestError.Err)
		}
		return nil, fmt.Errorf("API request failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		if err != nil {
			return nil, err
		}
		return nil, responseError(response.StatusCode, data)
	}
	return response, nil
}

func (c *client) get(ctx context.Context, path string, out any) error {
	response, err := c.do(ctx, requestPlan{Method: http.MethodGet, Path: path})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(out)
}

func responseError(status int, data []byte) error {
	var response struct {
		Error *model.Error `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil || response.Error == nil {
		return fmt.Errorf("HTTP %d returned an invalid error response", status)
	}
	return fmt.Errorf("HTTP %d %s: %s", status, response.Error.Code, response.Error.Message)
}

func projectPrefix(cfg config.Client) (string, error) {
	if !config.ValidID(cfg.Project) {
		return "", fmt.Errorf("a project ID is required through -p, VEX_PROJECT or configuration")
	}
	return "/v2/projects/" + url.PathEscape(cfg.Project), nil
}
