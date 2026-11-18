package httpkit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"aat/internal/telemetry"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestDistributedHTTPParentage(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	inner := httptest.NewServer(Observe("inner", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("traceparent") == "" {
			t.Error("traceparent not propagated")
		}
		if CorrelationID(r.Context()) != "trace-test" {
			t.Error("correlation lost")
		}
		w.WriteHeader(204)
	})))
	defer inner.Close()
	client := http.Client{Transport: telemetry.Transport{}}
	outer := Observe("outer", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, _ := http.NewRequestWithContext(r.Context(), "GET", inner.URL+"/nested", nil)
		request.Header.Set("X-Correlation-ID", CorrelationID(r.Context()))
		response, err := client.Do(request)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		w.WriteHeader(200)
	}))
	request := httptest.NewRequest("GET", "/outer?token=never-export", nil)
	request.Header.Set("X-Correlation-ID", "trace-test")
	request.Header.Set("Authorization", "Bearer never-export")
	request.Header.Set("traceparent", "00-11223344556677889900aabbccddeeff-1122334455667788-01")
	response := httptest.NewRecorder()
	outer.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("X-Trace-ID") != "11223344556677889900aabbccddeeff" {
		t.Fatalf("response: %+v", response)
	}
	spans := recorder.Ended()
	if len(spans) != 3 {
		t.Fatalf("spans=%d, want outer/client/inner", len(spans))
	}
	byID := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, span := range spans {
		byID[span.SpanContext().SpanID()] = span
		if span.SpanContext().TraceID().String() != "11223344556677889900aabbccddeeff" {
			t.Error("trace split")
		}
		for _, attr := range span.Attributes() {
			if attr.Value.AsString() == "never-export" || attr.Key == "url.full" {
				t.Fatal("sensitive attribute", attr)
			}
		}
	}
	var outerSpan, clientSpan, innerSpan sdktrace.ReadOnlySpan
	for _, span := range spans {
		switch span.Name() {
		case "GET /outer":
			outerSpan = span
		case "GET /nested":
			if span.SpanKind() == trace.SpanKindClient {
				clientSpan = span
			} else {
				innerSpan = span
			}
		}
	}
	if outerSpan == nil || clientSpan == nil || innerSpan == nil {
		t.Fatal("missing spans")
	}
	if clientSpan.Parent().SpanID() != outerSpan.SpanContext().SpanID() || innerSpan.Parent().SpanID() != clientSpan.SpanContext().SpanID() {
		t.Fatal("incorrect distributed parentage")
	}
}
