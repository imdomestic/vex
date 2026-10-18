package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/imdomestic/vex/controller/internal/githubapp"
	"github.com/spf13/pflag"
)

func githubCommand(ctx context.Context, global *pflag.FlagSet, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] != "create-app" {
		return fmt.Errorf("usage: vex github create-app --owner OWNER --name NAME --out DIRECTORY")
	}
	flags := commandFlags(global, "github create-app")
	owner := flags.String("owner", "", "GitHub organization")
	name := flags.String("name", "", "GitHub App name")
	directory := flags.String("out", "", "private output directory for App credentials")
	if err := parse(flags, args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *owner == "" || *name == "" || *directory == "" {
		return fmt.Errorf("create-app requires --owner, --name and --out")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	address := "http://" + listener.Addr().String()
	setup, err := githubapp.NewSetup(*owner, *name, *directory, address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: setup.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	defer server.Close()
	failure := make(chan error, 1)
	go func() { failure <- server.Serve(listener) }()
	if _, err = fmt.Fprintf(output, "GitHub App setup: %s\nCredentials directory: %s\nKeep this command running until installation is complete; press Ctrl+C to finish.\n", address, *directory); err != nil {
		return err
	}
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "open"
	}
	if err = exec.CommandContext(ctx, program, address).Run(); err != nil {
		return fmt.Errorf("cannot open the browser: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case err = <-setup.Errors():
		return err
	case err = <-failure:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
