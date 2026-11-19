package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

func waitForDatabase(ctx context.Context, ping func(context.Context) error) error {
	for {
		attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := ping(attempt)
		cancel()
		if err == nil {
			return nil
		}
		slog.Warn("database unavailable during startup; retrying")
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("database startup: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
