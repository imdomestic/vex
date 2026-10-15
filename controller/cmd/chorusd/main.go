package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/api"
	"github.com/imdomestic/chorus-testbed/controller/internal/archive"
	"github.com/imdomestic/chorus-testbed/controller/internal/assets"
	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/build"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/events"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/kube"
	"github.com/imdomestic/chorus-testbed/controller/internal/rooms"
	"github.com/imdomestic/chorus-testbed/controller/internal/slp"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func main() {
	syscall.Umask(0077)
	if err := run(); err != nil {
		slog.Error("chorusd stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 && args[0][0] != '-' {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", "/etc/chorusd/config.toml", "configuration file")
	id := flags.String("id", "", "build or room ID")
	roomCommand := flags.String("command", "", "RCON command")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	for _, dir := range []string{"state", "git", "work", "builds", "deps", "mods", "snapshots", "assets", ".work"} {
		if err = os.MkdirAll(filepath.Join(cfg.Server.DataDir, dir), 0700); err != nil {
			return err
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	db, err := store.Open(filepath.Join(cfg.Server.DataDir, "state", "chorusd.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if command == "rcon" || command == "probe" {
		manager, err := kube.New(cfg)
		if err != nil {
			return err
		}
		if command == "rcon" {
			result, err := manager.RCON(ctx, *id, *roomCommand)
			if err != nil {
				return err
			}
			fmt.Println(result)
			return nil
		}
		roomManager := rooms.New(cfg, db, nil, nil, manager, nil, nil)
		room, err := roomManager.Get(ctx, *id)
		if err != nil {
			return err
		}
		status, err := slp.Probe(ctx, cfg.Rooms.RouterAddress, room.Host, room.Port)
		if err != nil {
			return err
		}
		fmt.Printf("room=%s generation=%d build=%s matches=%t online=%d\n", room.ID, room.Generation, room.BuildID, slp.Matches(status, room), status.Players.Online)
		return nil
	}
	mirror, err := gitmirror.New(cfg.Git.Remote, filepath.Join(cfg.Server.DataDir, "git", "chorus.git"))
	if err != nil {
		return err
	}
	if command == "build-worker" {
		if *id == "" {
			return fmt.Errorf("build-worker requires --id")
		}
		return build.Worker(ctx, cfg, db, mirror, *id)
	}
	if command != "serve" {
		return fmt.Errorf("unknown command %q", command)
	}
	if err = mirror.Refresh(ctx); err != nil {
		return err
	}
	builder, err := build.New(cfg, db, mirror)
	if err != nil {
		return err
	}
	cluster, err := kube.New(cfg)
	if err != nil {
		return err
	}
	authenticator, err := auth.Load(cfg.Auth.UsersFile, cfg.Auth.SigningKeyFile)
	if err != nil {
		return err
	}
	bus := events.New(db)
	roomManager := rooms.New(cfg, db, mirror, builder, cluster, authenticator, bus)
	archiver, err := archive.New(cfg, db)
	if err != nil {
		return err
	}
	roomManager.Archive = archiver
	assetManager, err := assets.New(cfg, db)
	if err != nil {
		return err
	}
	roomManager.Assets = assetManager
	archiver.Assets = assetManager
	if err = roomManager.SyncBranches(ctx); err != nil {
		return err
	}
	server := api.New(cfg, db, mirror, builder, roomManager, authenticator, bus)
	errorsChannel := make(chan error, len(cfg.Server.Listen)+4)
	servers := []*http.Server{}
	listeners := []net.Listener{}
	for _, address := range cfg.Server.Listen {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			for _, open := range listeners {
				open.Close()
			}
			return err
		}
		listeners = append(listeners, listener)
		httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 * 1024}
		servers = append(servers, httpServer)
		go func() {
			err := httpServer.Serve(listener)
			if !errors.Is(err, http.ErrServerClosed) {
				errorsChannel <- err
			}
		}()
		slog.Info("chorusd listening", "address", address)
	}
	go func() { errorsChannel <- builder.Run(ctx) }()
	go func() { errorsChannel <- roomManager.Run(ctx) }()
	go func() { errorsChannel <- archiver.Run(ctx) }()
	go func() { errorsChannel <- bus.Run(ctx) }()
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errorsChannel:
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
