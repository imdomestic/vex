package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/imdomestic/vex/controller/internal/api"
	"github.com/imdomestic/vex/controller/internal/archive"
	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/events"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/runtime/kubernetes"
	"github.com/imdomestic/vex/controller/internal/slp"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/supervisor"
	"github.com/imdomestic/vex/controller/internal/transport"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func main() {
	syscall.Umask(0077)
	if err := run(); err != nil {
		slog.Error("vexd stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if command == "build-worker" {
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		job := flags.String("job", "", "absolute job description path")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if *job == "" || flags.NArg() != 0 {
			return fmt.Errorf("build-worker requires --job")
		}
		syscall.Umask(0007)
		return build.Worker(ctx, *job)
	}
	if command == "git-credential" {
		return gitCredential(args, os.Stdin, os.Stdout)
	}
	if command == "config" {
		if len(args) == 0 || (args[0] != "show" && args[0] != "check") {
			return fmt.Errorf("usage: vexd config show [--sources] | check")
		}
		action := args[0]
		withSources := slices.Contains(args[1:], "--sources")
		options := []string{}
		for _, arg := range args[1:] {
			if arg != "--sources" {
				options = append(options, arg)
			}
		}
		resolved, err := config.LoadDaemon(config.Options{Args: options})
		if err != nil {
			return err
		}
		if action == "show" {
			data, err := resolved.Render(withSources)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(data)
			return err
		}
		fmt.Println("configuration valid")
		if !resolved.Config.Minecraft.AcceptEULA {
			fmt.Println("minecraft.accept_eula=false: room starts return eula_not_accepted")
		}
		return nil
	}
	if command == "rcon" || command == "probe" {
		return roomOperation(ctx, command, args)
	}
	if command == "migrate" {
		return migrateCommand(ctx, args)
	}
	if command == "user" {
		return userCommand(ctx, args, os.Stdout)
	}
	if command != "serve" {
		return fmt.Errorf("unknown command %q", command)
	}
	resolved, err := config.LoadDaemon(config.Options{Args: args})
	if err != nil {
		return err
	}
	return serve(ctx, cancel, resolved.Config)
}

func serve(ctx context.Context, cancel context.CancelFunc, cfg config.Config) error {
	for _, dir := range []string{"state", "projects", "jobs", ".work"} {
		if err := os.MkdirAll(filepath.Join(cfg.Server.DataDir, dir), 0700); err != nil {
			return err
		}
	}
	db, err := store.Open(filepath.Join(cfg.Server.DataDir, "state", "vexd.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	authenticator, err := auth.NewWithSessions(ctx, cfg, db)
	if err != nil {
		return err
	}
	projectIDs := make([]string, 0, len(cfg.Projects))
	for id := range cfg.Projects {
		projectIDs = append(projectIDs, id)
	}
	slices.Sort(projectIDs)
	bus := events.New(db, projectIDs...)
	projects := map[string]*api.Server{}
	type loop struct {
		name string
		run  func(context.Context) error
	}
	loops := []loop{{"events", bus.Run}}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	var routerSource func(context.Context, string) (bool, error)
	roomSources := []func(context.Context, string) (bool, error){}
	for _, id := range projectIDs {
		project, err := cfg.ForProject(id)
		if err != nil {
			return err
		}
		scoped, err := db.WithProject(id)
		if err != nil {
			return err
		}
		credentials := gitmirror.Credentials{SSHIdentityFile: project.Git.SSHIdentityFile, KnownHostsFile: project.Git.KnownHostsFile, HTTPSTokenFile: project.Git.HTTPSTokenFile, HTTPSUsername: project.Git.HTTPSUsername, HelperBinary: executable}
		mirror, err := gitmirror.NewWithCredentials(project.Git.Remote, filepath.Join(project.DataDir, "git.git"), credentials)
		if err != nil {
			return err
		}
		if err = mirror.Initialize(ctx); err != nil {
			return err
		}
		builder, err := build.New(project, scoped, mirror)
		if err != nil {
			return err
		}
		backend, err := kubernetes.New(project)
		if err != nil {
			return err
		}
		routerSource = backend.RouterSource
		roomSources = append(roomSources, backend.RoomSource)
		manager := rooms.New(project, scoped, mirror, builder, backend, backend, authenticator, bus)
		if err = manager.InitializeRecords(ctx); err != nil {
			return err
		}
		archiver, err := archive.New(project, scoped)
		if err != nil {
			return err
		}
		worldManager, err := worlds.New(project, scoped)
		if err != nil {
			return err
		}
		manager.Archive, manager.Worlds, archiver.Worlds = archiver, worldManager, worldManager
		projects[id] = &api.Server{Cfg: project, DB: scoped, Git: mirror, Builds: builder, Rooms: manager}
		loops = append(loops, loop{id + "/builder", builder.Run}, loop{id + "/rooms", func(ctx context.Context) error {
			if err := backend.EnsureProject(ctx); err != nil {
				return err
			}
			return manager.Run(ctx)
		}}, loop{id + "/archive", archiver.Run}, loop{id + "/git", func(ctx context.Context) error {
			return refreshGit(ctx, manager, config.Duration(project.Git.PollInterval))
		}})
	}
	service := api.New(cfg, projects, authenticator, bus)
	service.RouterSource = routerSource
	internalSource := func(ctx context.Context, source string) (bool, error) {
		if routerSource != nil {
			allowed, err := routerSource(ctx, source)
			if err != nil || allowed {
				return allowed, err
			}
		}
		for _, allowedSource := range roomSources {
			allowed, err := allowedSource(ctx, source)
			if err != nil || allowed {
				return allowed, err
			}
		}
		return false, nil
	}
	var tlsConfig *tls.Config
	var reload <-chan os.Signal
	certificateFiles := transport.CertificateFiles{Certificate: cfg.Server.TLSCertFile, Key: cfg.Server.TLSKeyFile}
	if cfg.Server.TLSCertFile != "" {
		tlsConfig, err = certificateFiles.Config()
		if err != nil {
			return err
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGHUP)
		defer signal.Stop(signals)
		reload = signals
	}
	endpoints := cfg.Server.Endpoints()
	errorsChannel := make(chan error, len(endpoints)+1)
	servers := []*http.Server{}
	listeners := []net.Listener{}
	group, err := user.LookupGroup("vex-admin")
	if err != nil {
		return fmt.Errorf("admin socket requires the vex-admin group")
	}
	groupID, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	adminListener, err := auth.ListenAdminSocket(cfg.Auth.AdminSocket, groupID)
	if err != nil {
		return err
	}
	defer adminListener.Close()
	adminServer := newHTTPServer(ctx, service.AdminHandler())
	servers = append(servers, adminServer)
	listeners = append(listeners, adminListener)
	go func() {
		if err := adminServer.Serve(adminListener); !errors.Is(err, http.ErrServerClosed) {
			errorsChannel <- err
		}
	}()
	for _, endpoint := range endpoints {
		listener, err := net.Listen("tcp", endpoint.Address)
		if err != nil {
			for _, open := range listeners {
				open.Close()
			}
			return err
		}
		listeners = append(listeners, listener)
		handler := service.Handler()
		if endpoint.Scope == "internal" {
			handler = service.InternalHandler(internalSource)
		}
		httpServer := newHTTPServer(ctx, handler)
		if endpoint.TLS {
			httpServer.TLSConfig = tlsConfig.Clone()
		}
		servers = append(servers, httpServer)
		go func() {
			var err error
			if endpoint.TLS {
				err = httpServer.ServeTLS(listener, "", "")
			} else {
				err = httpServer.Serve(listener)
			}
			if !errors.Is(err, http.ErrServerClosed) {
				errorsChannel <- err
			}
		}()
		slog.Info("vexd listening", "address", endpoint.Address, "tls", endpoint.TLS, "scope", endpoint.Scope)
	}
	for _, operation := range loops {
		go supervisor.Run(ctx, operation.name, operation.run)
	}
	var runErr error
	for ctx.Err() == nil && runErr == nil {
		select {
		case <-ctx.Done():
		case runErr = <-errorsChannel:
		case <-reload:
			if _, err := certificateFiles.Load(); err != nil {
				slog.Error("TLS certificate reload failed", "error", err)
			} else {
				slog.Info("TLS certificate reloaded")
			}
		}
	}
	cancel()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	for _, httpServer := range servers {
		if err = httpServer.Shutdown(shutdown); err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}

func newHTTPServer(ctx context.Context, handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 * 1024}
}

func refreshGit(ctx context.Context, manager *rooms.Manager, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := manager.Refresh(ctx); err != nil {
			return fmt.Errorf("git refresh unavailable; retaining local mirror: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func gitCredential(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("git-credential", flag.ContinueOnError)
	filename := flags.String("token-file", "", "project token file")
	username := flags.String("username", "x-access-token", "HTTPS username")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 || !slices.Contains([]string{"get", "store", "erase"}, flags.Arg(0)) {
		return fmt.Errorf("invalid credential helper operation")
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(input, 1024*1024)); err != nil {
		return err
	}
	if flags.Arg(0) != "get" {
		return nil
	}
	if !filepath.IsAbs(*filename) || strings.ContainsAny(*username, "\r\n\x00") {
		return fmt.Errorf("invalid credential helper parameters")
	}
	data, err := os.ReadFile(*filename)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return fmt.Errorf("invalid HTTPS token file")
	}
	_, err = fmt.Fprintf(output, "username=%s\npassword=%s\n\n", *username, token)
	return err
}

func roomOperation(ctx context.Context, command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", "/etc/vex/vexd.toml", "configuration file")
	projectID := flags.String("project", "", "project ID")
	id := flags.String("id", "", "room ID")
	rcon := flags.String("command", "", "RCON command")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resolved, err := config.LoadDaemon(config.Options{Args: []string{"--config", *configPath}})
	if err != nil {
		return err
	}
	project, err := resolved.Config.ForProject(*projectID)
	if err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(resolved.Config.Server.DataDir, "state", "vexd.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	scoped, err := db.WithProject(*projectID)
	if err != nil {
		return err
	}
	backend, err := kubernetes.New(project)
	if err != nil {
		return err
	}
	manager := rooms.New(project, scoped, nil, nil, backend, backend, nil, nil)
	room, err := manager.Get(ctx, *id)
	if err != nil {
		return err
	}
	if command == "rcon" {
		ref, err := backend.Reference(room)
		if err != nil {
			return err
		}
		result, err := backend.RCON(ctx, ref, *rcon)
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	}
	status, err := slp.Probe(ctx, project.Server.RouterAddress, room.Host, room.Port)
	if err != nil {
		return err
	}
	fmt.Printf("project=%s room=%s generation=%d build=%s matches=%t online=%d\n", project.ID, room.ID, room.Generation, room.BuildID, slp.Matches(status, room), status.Players.Online)
	return nil
}
