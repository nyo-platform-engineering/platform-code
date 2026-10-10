// Package dbtrace logs PostgreSQL latency without SQL, parameters, or secrets.
package dbtrace

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"aat/internal/httpkit"
	"github.com/jackc/pgx/v5/tracelog"
)

func New() *tracelog.TraceLog {
	return &tracelog.TraceLog{Logger: tracelog.LoggerFunc(logCall), LogLevel: tracelog.LogLevelInfo}
}

func logCall(ctx context.Context, _ tracelog.LogLevel, operation string, data map[string]any) {
	id := httpkit.CorrelationID(ctx)
	if id == "" {
		id = httpkit.ID()
	}
	elapsed, _ := data["time"].(time.Duration)
	status, level := "ok", slog.LevelInfo
	if data["err"] != nil {
		status, level = "error", slog.LevelError
	}
	// Whitelist fields: driver SQL, arguments, and error details can contain sensitive data.
	slog.Log(ctx, level, "outbound_database",
		"service", "Aggregator", "upstream", "postgres",
		"operation", strings.ToLower(operation), "correlation_id", id,
		"latency_ms", float64(elapsed.Microseconds())/1000, "status", status)
}

// Observe covers PingContext, which bypasses the driver's query tracer.
func Observe(ctx context.Context, operation string, fn func(context.Context) error) error {
	if httpkit.CorrelationID(ctx) == "" {
		ctx = httpkit.WithCorrelationID(ctx, httpkit.ID())
	}
	started := time.Now()
	err := fn(ctx)
	logCall(ctx, tracelog.LogLevelInfo, operation, map[string]any{"time": time.Since(started), "err": err})
	return err
}
