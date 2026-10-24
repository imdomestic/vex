package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("vex-bundle", flag.ContinueOnError)
	root := flags.String("root", os.Getenv("GITHUB_WORKSPACE"), "checked out repository")
	manifestPath := flags.String("manifest", "vex.toml", "manifest relative path")
	output := flags.String("output", ".work/vex-bundle", "new bundle relative path")
	mode := flags.String("mode", "build", "bundle operation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *mode != "build" || *root == "" || strings.ContainsAny(*root, "\x00\r\n") {
		return fmt.Errorf("build mode requires a repository root and valid options")
	}
	if err := securePath(*root, *manifestPath, false); err != nil {
		return err
	}
	filename := filepath.Join(*root, filepath.FromSlash(*manifestPath))
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return fmt.Errorf("manifest must be a regular file no larger than 1 MiB")
	}
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err = errors.Join(readErr, file.Close()); err != nil {
		return err
	}
	doc, err := parseManifest(data)
	if err != nil {
		return err
	}
	bundle, err := collect(ctx, *root, doc, *output)
	if err != nil {
		return err
	}
	directory := filepath.Join(*root, filepath.FromSlash(*output))
	if destination := os.Getenv("GITHUB_OUTPUT"); destination != "" {
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		_, writeErr := fmt.Fprintf(file, "bundle-directory=%s\n", directory)
		if err = errors.Join(writeErr, file.Close()); err != nil {
			return err
		}
	}
	fmt.Printf("Collected %d loaders into %s\n", len(bundle.Loaders), directory)
	return nil
}
