package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// A returning worker may have stale Citus metadata while runtime reconciliation
// catches up. Keep the process starting instead of repeatedly entering CrashLoop.
func waitForSchema(ctx context.Context, phase string, prepare func(context.Context) error) error {
	for {
		err := prepare(ctx)
		if err == nil {
			return nil
		}
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || !(strings.HasPrefix(pg.Code, "08") || pg.Code == "57P03" ||
			pg.Code == "40001" || pg.Code == "40P01" || pg.Code == "55P03" ||
			(pg.Code == "55000" && strings.Contains(pg.Message, "metadata node") && strings.Contains(pg.Message, "out of sync"))) {
			return err
		}
		slog.Warn("schema unavailable during startup; retrying", "phase", phase, "sqlstate", pg.Code)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s startup: %w", phase, ctx.Err())
		case <-timer.C:
		}
	}
}
