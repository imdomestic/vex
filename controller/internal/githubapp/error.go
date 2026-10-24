package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/go-github/v88/github"
)

type RemoteError struct {
	Operation string
	Status    int
	RetryAt   time.Time
	cause     error
}

func (e *RemoteError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("GitHub %s failed (HTTP %d)", e.Operation, e.Status)
	}
	if e.cause != nil {
		return "GitHub " + e.Operation + " interrupted"
	}
	return "GitHub " + e.Operation + " failed"
}

func (e *RemoteError) Unwrap() error { return e.cause }

func safeRemoteError(operation string, err error, now time.Time) *RemoteError {
	result := &RemoteError{Operation: operation}
	var remote *RemoteError
	var primary *github.RateLimitError
	var secondary *github.AbuseRateLimitError
	var failure *github.ErrorResponse
	var response *http.Response
	switch {
	case errors.As(err, &remote):
		result.Status, result.RetryAt, result.cause = remote.Status, remote.RetryAt, remote.cause
	case errors.As(err, &primary):
		response, result.RetryAt = primary.Response, primary.Rate.Reset.Time
	case errors.As(err, &secondary):
		response = secondary.Response
		if secondary.RetryAfter != nil && *secondary.RetryAfter >= 0 {
			result.RetryAt = now.Add(*secondary.RetryAfter)
		}
	case errors.As(err, &failure):
		response = failure.Response
	case errors.Is(err, context.Canceled):
		result.cause = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		result.cause = context.DeadlineExceeded
	}
	if response != nil {
		result.Status = response.StatusCode
		if retry := ResponseRetryAt(response, now); retry.After(result.RetryAt) {
			result.RetryAt = retry
		}
	}
	return result
}

func ResponseRetryAt(response *http.Response, now time.Time) time.Time {
	var until time.Time
	if value := response.Header.Get("Retry-After"); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
			until = now.Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(value); err == nil {
			until = date
		}
	}
	if response.Header.Get("X-RateLimit-Remaining") == "0" {
		if seconds, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if reset := time.Unix(seconds, 0); reset.After(until) {
				until = reset
			}
		}
	}
	if (response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden) && !until.After(now) {
		until = now.Add(time.Minute)
	}
	return until
}
