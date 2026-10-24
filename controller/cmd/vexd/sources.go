package main

import (
	"context"
	"errors"
	"time"
)

func allowedInternalSource(ctx context.Context, address string, checks []func(context.Context, string) (bool, error)) (bool, error) {
	observing, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	type result struct {
		allowed bool
		err     error
	}
	results := make(chan result, len(checks))
	for _, check := range checks {
		go func() {
			allowed, err := check(observing, address)
			results <- result{allowed, err}
		}()
	}
	var failures []error
	for range checks {
		select {
		case <-observing.Done():
			return false, errors.Join(append(failures, ctx.Err())...)
		case result := <-results:
			if result.allowed && result.err == nil {
				return true, nil
			}
			if result.err != nil && !errors.Is(result.err, context.DeadlineExceeded) {
				failures = append(failures, result.err)
			}
		}
	}
	return false, errors.Join(failures...)
}
