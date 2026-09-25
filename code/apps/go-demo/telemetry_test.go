package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type contextHandler struct{ traces *[]trace.SpanContext }

func (h contextHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h contextHandler) Handle(ctx context.Context, _ slog.Record) error {
	*h.traces = append(*h.traces, trace.SpanContextFromContext(ctx))
	return nil
}
func (h contextHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h contextHandler) WithGroup(string) slog.Handler      { return h }

func TestDemoProducesParentedErrorAndContextualLogs(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	contexts := []trace.SpanContext{}
	handler := newHandler(slog.New(contextHandler{&contexts}))
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest("POST", "/demo/error", nil))
	var response map[string]string
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	if out.Code != 500 || len(spans) != 2 || len(contexts) != 2 {
		t.Fatalf("status=%d spans=%d logs=%d", out.Code, len(spans), len(contexts))
	}
	var root, child sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.SpanKind() == trace.SpanKindServer {
			root = span
		} else {
			child = span
		}
	}
	if root == nil || child == nil || child.Parent().SpanID() != root.SpanContext().SpanID() || root.Status().Code != codes.Error || child.Status().Code != codes.Error {
		t.Fatal("expected parented error spans")
	}
	for _, sc := range contexts {
		if sc.TraceID() != root.SpanContext().TraceID() || !sc.IsValid() {
			t.Fatal("log correlation missing")
		}
	}
	if response["trace_id"] != root.SpanContext().TraceID().String() {
		t.Fatal("response trace ID differs")
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	if len(recorder.Ended()) != 2 {
		t.Fatal("health probes must not create spans")
	}
}
func TestTelemetryDisabledWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	got, shutdown, err := setupTelemetry(context.Background(), logger)
	if err != nil || got != logger {
		t.Fatal("standalone logger must remain available")
	}
	shutdown()
}
