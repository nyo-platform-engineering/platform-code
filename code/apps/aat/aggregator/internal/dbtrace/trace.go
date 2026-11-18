// Package dbtrace traces and logs PostgreSQL latency without SQL, parameters, or secrets.
package dbtrace

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/tracelog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Tracer struct{ log *tracelog.TraceLog }

func New() *Tracer {
	return &Tracer{log: &tracelog.TraceLog{Logger: tracelog.LoggerFunc(logCall), LogLevel: tracelog.LogLevelInfo}}
}

func (t *Tracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, span := telemetry.Start(ctx, "postgres query", trace.SpanKindClient)
	span.SetAttributes(attribute.String("db.system.name", "postgresql"))
	return t.log.TraceQueryStart(ctx, conn, data)
}

func (t *Tracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	t.log.TraceQueryEnd(ctx, conn, data)
	span := trace.SpanFromContext(ctx)
	telemetry.Fail(span, data.Err)
	span.End()
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
		"trace_id", telemetry.TraceID(ctx),
		"latency_ms", float64(elapsed.Microseconds())/1000, "status", status)
}

// Observe covers PingContext, which bypasses the driver's query tracer.
func Observe(ctx context.Context, operation string, fn func(context.Context) error) error {
	if httpkit.CorrelationID(ctx) == "" {
		ctx = httpkit.WithCorrelationID(ctx, httpkit.ID())
	}
	ctx, span := telemetry.Start(ctx, "postgres "+operation, trace.SpanKindClient)
	defer span.End()
	span.SetAttributes(attribute.String("db.system.name", "postgresql"))
	started := time.Now()
	err := fn(ctx)
	telemetry.Fail(span, err)
	logCall(ctx, tracelog.LogLevelInfo, operation, map[string]any{"time": time.Since(started), "err": err})
	return err
}
