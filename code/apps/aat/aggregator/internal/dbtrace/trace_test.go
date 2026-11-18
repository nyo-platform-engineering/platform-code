package dbtrace

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"aat/internal/httpkit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/tracelog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestQuerySpansDescribeOperationWithoutValues(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	tracer := New()
	tracer.log.LogLevel = tracelog.LogLevelNone
	root := httpkit.WithCorrelationID(context.Background(), "trace-visibility-test")
	root = context.WithValue(root, queryInfoKey{}, queryInfo{"SELECT", "hazard_events"})
	ctx := tracer.TraceQueryStart(root, nil, pgx.TraceQueryStartData{SQL: `SELECT * FROM "hazard_events" WHERE source = $1`, Args: []any{"secret-bound-value"}})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 7")})
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatal("expected one query span")
	}
	span := spans[0]
	attrs := attributesMap(span.Attributes())
	if span.Name() != "SELECT hazard_events" || attrs["db.operation.name"] != "SELECT" || attrs["db.collection.name"] != "hazard_events" || attrs["db.response.returned_rows"] != int64(7) || attrs["aat.correlation_id"] != "trace-visibility-test" {
		t.Fatalf("span: %s %+v", span.Name(), attrs)
	}
	for key, value := range attrs {
		if strings.Contains(key, "parameter") || strings.Contains(strings.TrimSpace(strings.ToLower(fmt.Sprint(value))), "secret-bound-value") {
			t.Fatalf("bound parameter leaked: %s=%v", key, value)
		}
	}
	ctx = tracer.TraceQueryStart(root, nil, pgx.TraceQueryStartData{SQL: `INSERT INTO hazard_events (id) VALUES ($1)`, Args: []any{"private-id"}})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: "private-error-detail"}})
	span = recorder.Ended()[1]
	attrs = attributesMap(span.Attributes())
	if span.Status().Code != codes.Error || attrs["error.type"] != "23505" {
		t.Fatalf("error span: %+v %+v", span.Status(), attrs)
	}
	for _, attr := range span.Attributes() {
		if strings.Contains(attr.Value.AsString(), "private-") {
			t.Fatalf("error data leaked: %v", attr)
		}
	}
}
