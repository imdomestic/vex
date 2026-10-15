package supervisor

import (
	"context"
	"log/slog"
	"time"
)

func Delay(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	if failures > 6 {
		return time.Minute
	}
	return time.Second << (failures - 1)
}

func Run(ctx context.Context, name string, run func(context.Context) error) {
	failures := 0
	for ctx.Err() == nil {
		started := time.Now()
		err := run(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= time.Minute {
			failures = 0
		}
		failures++
		delay := Delay(failures)
		slog.Error("background loop stopped; retry scheduled", "loop", name, "error", err, "retry_after", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
