package ci

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/imdomestic/vex/controller/internal/githubapp"
)

type apiError struct {
	operation string
	status    int
	retryAt   time.Time
}

func (e *apiError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("GitHub %s failed (HTTP %d)", e.operation, e.status)
	}
	return "GitHub " + e.operation + " failed"
}

func apiFailure(operation string, err error) error {
	var previous *apiError
	if errors.As(err, &previous) {
		return &apiError{operation: operation, status: previous.status, retryAt: previous.retryAt}
	}
	remote := githubapp.SafeError(operation, err).(*githubapp.RemoteError)
	return &apiError{operation: operation, status: remote.Status, retryAt: remote.RetryAt}
}

type cachedPage struct {
	header http.Header
	body   []byte
}

type conditionalTransport struct {
	base         http.RoundTripper
	mu           sync.Mutex
	pages        map[string]cachedPage
	blockedUntil time.Time
	notModified  int
	mutationAt   time.Time
}

func newConditionalClient(source *github.Client) (*github.Client, *conditionalTransport, error) {
	client := source.Client()
	transport := &conditionalTransport{base: client.Transport, pages: map[string]cachedPage{}}
	if transport.base == nil {
		transport.base = http.DefaultTransport
	}
	client.Transport = transport
	wrapped, err := github.NewClient(github.WithHTTPClient(client))
	return wrapped, transport, err
}

func (t *conditionalTransport) blocked() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.blockedUntil
}

func (t *conditionalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	if time.Now().Before(t.blockedUntil) {
		until := t.blockedUntil
		t.mu.Unlock()
		return nil, &apiError{operation: "request deferred by rate limit", retryAt: until}
	}
	key := request.URL.String()
	cached, exists := t.pages[key]
	var wait time.Duration
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		now := time.Now()
		if next := t.mutationAt.Add(time.Second); next.After(now) {
			wait = next.Sub(now)
		}
		t.mutationAt = now.Add(wait)
	}
	t.mu.Unlock()
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-timer.C:
		}
	}
	cacheable := request.Method == http.MethodGet && request.URL.Scheme == "https" && request.URL.Host == "api.github.com" && (strings.HasSuffix(request.URL.Path, "/pulls") || strings.Contains(request.URL.Path, "/actions/workflows/") && strings.HasSuffix(request.URL.Path, "/runs"))
	if cacheable && exists && cached.header.Get("ETag") != "" {
		request = request.Clone(request.Context())
		request.Header.Set("If-None-Match", cached.header.Get("ETag"))
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if until := responseBackoff(response, time.Now()); !until.IsZero() {
		t.mu.Lock()
		if until.After(t.blockedUntil) {
			t.blockedUntil = until
		}
		t.mu.Unlock()
	}
	if !cacheable {
		return response, nil
	}
	if response.StatusCode == http.StatusNotModified && exists {
		if err = response.Body.Close(); err != nil {
			return nil, err
		}
		header := cached.header.Clone()
		for name, values := range response.Header {
			header[name] = append([]string{}, values...)
		}
		response.StatusCode, response.Status = http.StatusOK, "200 OK"
		response.Header, response.Body, response.ContentLength = header, io.NopCloser(bytes.NewReader(cached.body)), int64(len(cached.body))
		response.Header.Set("Content-Length", strconv.Itoa(len(cached.body)))
		t.mu.Lock()
		t.notModified++
		t.mu.Unlock()
		return response, nil
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") == "" {
		return response, nil
	}
	const maximum = 16 << 20
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(body) > maximum {
		return nil, fmt.Errorf("GitHub list response exceeds 16 MiB")
	}
	t.mu.Lock()
	if len(t.pages) >= 1024 {
		t.pages = map[string]cachedPage{}
	}
	t.pages[key] = cachedPage{header: response.Header.Clone(), body: body}
	t.mu.Unlock()
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func responseBackoff(response *http.Response, now time.Time) time.Time {
	return githubapp.ResponseRetryAt(response, now)
}
