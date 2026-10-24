package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/spf13/pflag"
)

func userCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "github-id" {
		return githubIdentityCommand(ctx, args[1:], output)
	}
	if len(args) < 2 || args[0] != "invite" && args[0] != "sessions" {
		return fmt.Errorf("usage: vexd user invite NAME [--ttl DURATION] | sessions NAME [--revoke ID]")
	}
	flags := pflag.NewFlagSet("user "+args[0], pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configFile := flags.String("config", "", "operator configuration")
	socket := flags.String("socket", "", "Unix admin socket")
	ttl := flags.String("ttl", "", "invitation expiration")
	revoke := flags.String("revoke", "", "session ID to revoke")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 1 || !config.ValidID(flags.Arg(0)) {
		return fmt.Errorf("user command requires a valid user name")
	}
	if args[0] == "invite" && *revoke != "" || args[0] == "sessions" && *ttl != "" {
		return fmt.Errorf("invalid user command option")
	}
	if *socket == "" {
		options := config.Options{}
		if *configFile != "" {
			options.Args = []string{"--config", *configFile}
		}
		resolved, err := config.LoadDaemon(options)
		if err != nil {
			return err
		}
		*socket = resolved.Config.Auth.AdminSocket
	}
	method, path := http.MethodGet, "/v2/admin/users/"+url.PathEscape(flags.Arg(0))+"/sessions"
	var input io.Reader
	if args[0] == "invite" {
		method, path = http.MethodPost, "/v2/auth/invites"
		data, err := json.Marshal(map[string]string{"user": flags.Arg(0), "ttl": *ttl})
		if err != nil {
			return err
		}
		input = bytes.NewReader(data)
	} else if *revoke != "" {
		method, path = http.MethodDelete, path+"/"+url.PathEscape(*revoke)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://vex-admin"+path, input)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", *socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("cannot connect to Vex admin socket; membership in vex-admin is required")
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("admin operation failed with HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(output, io.LimitReader(response.Body, 1024*1024))
	return err
}
