// Package dbtrace exports sanitized SQL spans and logs database latency without query values.
package dbtrace

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	if span.IsRecording() {
		name, attrs := queryDetails(data.SQL)
		span.SetName(name)
		span.SetAttributes(attrs...)
	}
	if info, ok := ctx.Value(queryInfoKey{}).(queryInfo); ok {
		if info.operation != "" {
			span.SetAttributes(attribute.String("db.operation.name", info.operation))
		}
		if info.table != "" {
			span.SetAttributes(attribute.String("db.collection.name", info.table))
		}
	}
	if conn != nil {
		config := conn.Config()
		span.SetAttributes(attribute.String("db.namespace", config.Database), attribute.String("server.address", config.Host), attribute.Int("server.port", int(config.Port)))
	}
	if id := httpkit.CorrelationID(ctx); id != "" {
		span.SetAttributes(attribute.String("aat.correlation_id", id))
	}
	return t.log.TraceQueryStart(ctx, conn, data)
}

func (t *Tracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	t.log.TraceQueryEnd(ctx, conn, data)
	span := trace.SpanFromContext(ctx)
	// CommandTag is supplied by PostgreSQL, so the operation is not guessed from SQL.
	fields := strings.Fields(data.CommandTag.String())
	operation := []string{}
	for _, field := range fields {
		if _, err := strconv.ParseInt(field, 10, 64); err == nil {
			break
		}
		operation = append(operation, field)
	}
	if len(operation) > 0 {
		span.SetAttributes(attribute.String("db.operation.name", strings.Join(operation, " ")))
	}
	if data.Err == nil && data.CommandTag.Select() {
		span.SetAttributes(attribute.Int64("db.response.returned_rows", data.CommandTag.RowsAffected()))
	}
	var pgError *pgconn.PgError
	if errors.As(data.Err, &pgError) {
		span.SetAttributes(attribute.String("error.type", pgError.Code))
	}
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
