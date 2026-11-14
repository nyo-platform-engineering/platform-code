package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDemoParameterValidation(t *testing.T) {
	for _, query := range []string{"delay_ms=-1", "delay_ms=2001", "delay_ms=NaN", "quantity=0", "quantity=11", "region=unknown", "source=unknown", "quantity=1&quantity=2"} {
		if _, err := parseDemoParameters(httptest.NewRequest("POST", "/demo/success?"+query, nil), "success"); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
	defaults, err := parseDemoParameters(httptest.NewRequest("POST", "/demo/slow", nil), "slow")
	if err != nil || defaults.DelayMS != 500 {
		t.Fatal("manual slow default changed")
	}
	handler := newHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest("POST", "/demo/success?delay_ms=2001", nil))
	if out.Code != 400 {
		t.Fatal("unbounded delay accepted")
	}
}
func TestBotAttributesReachBothSpansAndLogs(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	var logs bytes.Buffer
	handler := newHandler(slog.New(slog.NewJSONHandler(&logs, nil)))
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest("POST", "/demo/success?source=bot&region=eu-west-1&quantity=4&delay_ms=0", nil))
	if out.Code != 200 || len(recorder.Ended()) != 2 {
		t.Fatal("missing demo spans")
	}
	for _, span := range recorder.Ended() {
		attrs := map[string]any{}
		for _, attr := range span.Attributes() {
			attrs[string(attr.Key)] = attr.Value.AsInterface()
		}
		if attrs["demo.source"] != "bot" || attrs["demo.region"] != "eu-west-1" || attrs["demo.quantity"] != int64(4) || attrs["demo.payload"] == nil {
			t.Fatalf("missing filter attributes: %#v", attrs)
		}
	}
	if !strings.Contains(logs.String(), `"demo.source":"bot"`) || !strings.Contains(logs.String(), `"demo.payload"`) {
		t.Fatal("missing searchable log attributes")
	}
}
