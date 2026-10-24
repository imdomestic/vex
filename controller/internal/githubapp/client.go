package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
	"github.com/imdomestic/vex/controller/internal/config"
	"golang.org/x/sys/unix"
)

type App struct {
	mu        sync.Mutex
	ID        int64
	transport *ghinstallation.AppsTransport
	client    *github.Client
	clients   map[string]*github.Client
	tokens    map[string]*ghinstallation.Transport
	Slug      string
}

func (a *App) BotLogin() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Slug + "[bot]"
}

func New(cfg config.GitHubApp) (*App, error) {
	transport, err := ghinstallation.NewAppsTransportKeyFromFile(http.DefaultTransport, cfg.AppID, cfg.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("cannot load GitHub App private key")
	}
	client, err := github.NewClient(github.WithHTTPClient(&http.Client{Transport: transport, Timeout: 30 * time.Second}))
	if err != nil {
		return nil, fmt.Errorf("cannot initialize GitHub App client")
	}
	return &App{ID: cfg.AppID, transport: transport, client: client, clients: map[string]*github.Client{}, tokens: map[string]*ghinstallation.Transport{}}, nil
}

func (a *App) Repository(ctx context.Context, repository string) (*github.Client, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid GitHub repository")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if client := a.clients[repository]; client != nil {
		return client, nil
	}
	app, _, err := a.client.Apps.Get(ctx, "")
	if err != nil {
		return nil, SafeError("read App", err)
	}
	a.Slug = app.GetSlug()
	installation, _, err := a.client.Apps.GetRepositoryInstallation(ctx, parts[0], parts[1])
	if err != nil {
		return nil, SafeError("read repository installation", err)
	}
	transport := ghinstallation.NewFromAppsTransport(a.transport, installation.GetID())
	transport.InstallationTokenOptions = &github.InstallationTokenOptions{
		Permissions:  &github.InstallationPermissions{Actions: github.Ptr("read"), Contents: github.Ptr("read"), PullRequests: github.Ptr("write"), Checks: github.Ptr("write"), Metadata: github.Ptr("read")},
		Repositories: []string{parts[1]},
	}
	client, err := github.NewClient(github.WithHTTPClient(&http.Client{Transport: transport, Timeout: 30 * time.Second}))
	if err != nil {
		return nil, fmt.Errorf("cannot initialize GitHub installation client")
	}
	a.clients[repository] = client
	a.tokens[repository] = transport
	return client, nil
}

func SafeError(operation string, err error) error {
	return safeRemoteError(operation, err, time.Now().UTC())
}

func ArtifactURL(value *url.URL) bool {
	if value == nil || value.Scheme != "https" || value.User != nil || value.Fragment != "" || value.Port() != "" && value.Port() != "443" {
		return false
	}
	host := strings.ToLower(value.Hostname())
	return strings.HasSuffix(host, ".blob.core.windows.net") || strings.HasSuffix(host, ".actions.githubusercontent.com") || strings.HasSuffix(host, ".githubusercontent.com")
}

func Download(ctx context.Context, client *github.Client, owner, repo string, artifact *github.Artifact, directory string) (string, error) {
	const maximum = int64(2 << 30)
	if artifact.GetExpired() || artifact.GetSizeInBytes() <= 0 || artifact.GetSizeInBytes() > maximum {
		return "", fmt.Errorf("artifact is expired or exceeds 2 GiB")
	}
	location, _, err := client.Actions.DownloadArtifact(ctx, owner, repo, artifact.GetID(), 0)
	if err != nil {
		return "", SafeError("resolve artifact download", err)
	}
	if !ArtifactURL(location) {
		return "", fmt.Errorf("GitHub artifact download host is not allowed")
	}
	request, err := http.NewRequestWithContext(ctx, "GET", location.String(), nil)
	if err != nil {
		return "", fmt.Errorf("invalid artifact download request")
	}
	httpClient := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !ArtifactURL(request.URL) {
			return fmt.Errorf("artifact redirect is not allowed")
		}
		return nil
	}}
	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("artifact download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.ContentLength > maximum {
		return "", fmt.Errorf("artifact response is invalid (HTTP %d)", response.StatusCode)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	var filesystem unix.Statfs_t
	if err = unix.Statfs(directory, &filesystem); err != nil {
		return "", err
	}
	if uint64(filesystem.Bavail)*uint64(filesystem.Bsize) < uint64(15<<30)+uint64(artifact.GetSizeInBytes()) {
		return "", fmt.Errorf("artifact download requires at least 15 GiB free after download")
	}
	file, err := os.CreateTemp(directory, "artifact-*.zip")
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(file.Name())
		}
	}()
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, maximum+1))
	if err != nil || written > maximum || written != artifact.GetSizeInBytes() {
		return "", fmt.Errorf("artifact size verification failed")
	}
	if artifact.GetDigest() == "" || artifact.GetDigest() != "sha256:"+hex.EncodeToString(digest.Sum(nil)) {
		return "", fmt.Errorf("artifact SHA256 verification failed; upload-artifact v4 or newer is required")
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	complete = true
	return file.Name(), nil
}
