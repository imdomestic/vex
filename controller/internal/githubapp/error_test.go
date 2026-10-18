package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
)

func TestSafeRemoteErrorRetainsOnlyStatusAndRequiredDelay(t *testing.T) {
	now := time.Unix(2000000000, 0).UTC()
	response := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"120"}, "X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"2000000180"}}, Request: &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.com", RawQuery: "sensitive-test-value"}}}
	original := &github.ErrorResponse{Response: response, Message: "sensitive-test-value"}
	result := safeRemoteError("read installation", original, now)
	if result.Status != http.StatusTooManyRequests || !result.RetryAt.Equal(now.Add(3*time.Minute)) {
		t.Fatal("GitHub required retry delay or status was lost")
	}
	if strings.Contains(fmt.Sprintf("%+v", result), "sensitive-test-value") || errors.Unwrap(result) != nil {
		t.Fatal("GitHub error retained upstream request or response details")
	}
	wrapped := safeRemoteError("installation access", fmt.Errorf("outer: %w", result), now.Add(time.Second))
	if wrapped.Status != result.Status || wrapped.RetryAt != result.RetryAt {
		t.Fatal("safe error wrapping lost retry metadata")
	}
	primary := safeRemoteError("read repository", &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: now.Add(5 * time.Minute)}}, Response: &http.Response{StatusCode: http.StatusForbidden}}, now)
	if !primary.RetryAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatal("SDK primary rate reset was lost")
	}
	delay := 4 * time.Minute
	secondary := safeRemoteError("write comment", &github.AbuseRateLimitError{RetryAfter: &delay, Response: &http.Response{StatusCode: http.StatusForbidden}}, now)
	if !secondary.RetryAt.Equal(now.Add(delay)) {
		t.Fatal("SDK secondary retry delay was lost")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		interrupted := SafeError("request", &url.Error{Op: "GET", URL: "https://api.github.com/?sensitive-test-value", Err: cause})
		if !errors.Is(interrupted, cause) || strings.Contains(interrupted.Error(), "sensitive-test-value") {
			t.Fatal("context cancellation or safe error boundary was lost")
		}
	}
}
