package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/spf13/pflag"
)

func loginCommand(ctx context.Context, global *pflag.FlagSet, args []string, input io.Reader, output io.Writer) error {
	flags := commandFlags(global, args[0])
	tokenStdin := flags.Bool("token-stdin", false, "read a service account token from standard input")
	ttl := flags.String("ttl", "", "invitation lifetime")
	clientName := flags.String("client", "", "session device name")
	if err := parse(flags, args[1:]); err != nil {
		return err
	}
	positionals := flags.Args()
	if args[0] != "login" && (*tokenStdin || *clientName != "") || args[0] != "auth" && args[0] != "user" && *ttl != "" {
		return fmt.Errorf("option does not apply to this login command")
	}
	settings := []string{}
	global.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			settings = append(settings, "--"+flag.Name+"="+flag.Value.String())
		}
	})
	resolved, err := config.LoadClient(config.Options{Args: settings, AllowMissing: args[0] == "login"})
	if err != nil {
		return err
	}
	if args[0] == "login" {
		if len(positionals) > 1 {
			return fmt.Errorf("usage: vex login [SERVER-URL|INVITATION-LINK] [--token-stdin]")
		}
		return login(ctx, global, resolved, positionals, *tokenStdin, *clientName, input, output)
	}
	if err = selectClient(global, &resolved); err != nil {
		return err
	}
	cfg := resolved.Config
	if args[0] == "logout" {
		if len(positionals) != 0 {
			return fmt.Errorf("logout accepts no positional arguments")
		}
		c, clientErr := newClient(cfg, false)
		if clientErr == nil {
			response, requestErr := c.do(ctx, requestPlan{Method: http.MethodPost, Path: "/v2/auth/logout"})
			if response != nil {
				response.Body.Close()
			}
			clientErr = requestErr
		}
		if err = credentialStore(cfg).Delete(cfg.URL); err != nil {
			if !errors.Is(err, auth.ErrKeyringCleanupUnavailable) {
				return err
			}
			fmt.Fprintln(output, "Local credential record removed; system keyring cleanup could not be confirmed.")
		}
		if clientErr != nil {
			fmt.Fprintln(output, "Server session revocation could not be confirmed; its configured expiry still applies.")
		}
		_, err = fmt.Fprintln(output, "Logged out locally.")
		return err
	}
	c, err := newClient(cfg, false)
	if err != nil {
		return err
	}
	plan := requestPlan{Method: http.MethodGet, Path: "/v2/me"}
	switch args[0] {
	case "whoami":
		if len(positionals) != 0 {
			return fmt.Errorf("whoami accepts no positional arguments")
		}
	case "auth", "user":
		user := ""
		if args[0] == "auth" {
			if len(positionals) != 1 || positionals[0] != "invite" {
				return fmt.Errorf("usage: vex auth invite [--ttl DURATION]")
			}
		} else {
			if len(positionals) != 2 || positionals[0] != "invite" || !config.ValidID(positionals[1]) {
				return fmt.Errorf("usage: vex user invite NAME [--ttl DURATION]")
			}
			user = positionals[1]
		}
		plan = requestPlan{Method: http.MethodPost, Path: "/v2/auth/invites", Body: map[string]string{"user": user, "ttl": *ttl}}
	}
	response, err := c.do(ctx, plan)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(output, response.Body)
	return err
}

func invitationAddress(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid server or invitation URL")
	}
	code := ""
	path := strings.TrimRight(u.Path, "/")
	if prefix, suffix, found := strings.Cut(path, "/i/"); found {
		if suffix == "" || strings.Contains(suffix, "/") {
			return "", "", fmt.Errorf("invalid invitation URL path")
		}
		code, u.Path, u.RawPath = suffix, prefix, ""
	}
	server, err := auth.NormalizeServer(u.String())
	return server, code, err
}

func login(ctx context.Context, global *pflag.FlagSet, resolved config.Resolved[config.Client], args []string, tokenStdin bool, clientName string, input io.Reader, output io.Writer) error {
	cfg := resolved.Config
	server, code := "", ""
	var err error
	if len(args) == 1 {
		server, code, err = invitationAddress(args[0])
	} else {
		err = selectClient(global, &resolved)
		if err == nil {
			server, err = auth.NormalizeServer(resolved.Config.URL)
		}
	}
	if err != nil {
		return err
	}
	if tokenStdin && code != "" {
		return fmt.Errorf("token input cannot be combined with an invitation")
	}
	publicConfig := cfg
	publicConfig.URL, publicConfig.TokenFile, publicConfig.Path = server, "", ""
	c, err := newClient(publicConfig, true)
	if err != nil {
		return err
	}
	var meta model.Meta
	if err = c.get(ctx, "/v2/meta", &meta); err != nil {
		return err
	}
	if strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("server metadata has no name")
	}
	var user model.User
	issued := false
	if tokenStdin {
		data, err := io.ReadAll(io.LimitReader(input, 514))
		if err != nil {
			return fmt.Errorf("cannot read token input")
		}
		c.token = strings.TrimSpace(string(data))
		if c.token == "" || len(c.token) > 512 || strings.ContainsAny(c.token, "\r\n\x00") {
			return fmt.Errorf("invalid token input")
		}
		if err = c.get(ctx, "/v2/me", &user); err != nil {
			return err
		}
	} else {
		available := false
		for _, method := range meta.Auth.Methods {
			available = available || method == "invite"
		}
		if !available {
			return fmt.Errorf("server does not offer invitation login")
		}
		if code == "" {
			if _, err = fmt.Fprint(output, "Invitation code: "); err != nil {
				return err
			}
			code, err = bufio.NewReader(io.LimitReader(input, 64)).ReadString('\n')
			if err != nil && err != io.EOF {
				return fmt.Errorf("cannot read invitation code")
			}
			code = strings.TrimSpace(code)
		}
		if clientName == "" {
			hostname, err := os.Hostname()
			if err != nil {
				return err
			}
			clientName = "vex on " + hostname
		}
		response, err := c.do(ctx, requestPlan{Method: http.MethodPost, Path: "/v2/auth/invites/redeem", Body: map[string]string{"code": code, "client": clientName}})
		if err != nil {
			return err
		}
		var result auth.Login
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result)
		response.Body.Close()
		if decodeErr != nil || result.Token == "" || result.User.ID == "" {
			return fmt.Errorf("invalid login response")
		}
		c.token, user, issued = result.Token, result.User, true
	}
	saved := false
	defer func() {
		if issued && !saved {
			response, err := c.do(ctx, requestPlan{Method: http.MethodPost, Path: "/v2/auth/logout"})
			if err == nil {
				response.Body.Close()
			}
		}
	}()
	name := cfg.Server
	if name == "" {
		for candidate, entry := range cfg.Servers {
			canonical, err := auth.NormalizeServer(entry.URL)
			if err == nil && canonical == server {
				name = candidate
				break
			}
		}
	}
	if name == "" {
		digest := sha256.Sum256([]byte(server))
		name = "server-" + hex.EncodeToString(digest[:6])
	}
	if previous, exists := cfg.Servers[name]; exists {
		canonical, err := auth.NormalizeServer(previous.URL)
		if err != nil || canonical != server {
			return fmt.Errorf("server ID already belongs to a different URL")
		}
	}
	backend, err := credentialStore(cfg).Save(server, c.token)
	if err != nil {
		return err
	}
	entry := cfg.Servers[name]
	entry.URL, entry.Name = server, meta.Name
	if cfg.Project != "" {
		entry.Project = cfg.Project
	}
	if err = config.SaveClientServer(resolved.Path, name, entry); err != nil {
		return err
	}
	saved = true
	if backend == "file" {
		fmt.Fprintln(output, "System keyring unavailable; credential saved in a user-owned file with mode 0600.")
	}
	_, err = fmt.Fprintf(output, "Logged in as %s on %s (%s).\n", user.ID, meta.Name, name)
	return err
}
