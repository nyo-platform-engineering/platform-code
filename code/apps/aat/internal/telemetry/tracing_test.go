package telemetry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestOTLPExportFromHTTPChain(t *testing.T) {
	var mu sync.Mutex
	var count int
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Error("incorrect OTLP path", r.URL.Path)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var request collector.ExportTraceServiceRequest
		if err = proto.Unmarshal(data, &request); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, resource := range request.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				count += len(scope.Spans)
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer sink.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_on")
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	shutdown, err := telemetry.Init(context.Background(), "export-test")
	if err != nil {
		t.Fatal(err)
	}
	inner := httptest.NewServer(httpkit.Observe("inner", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	defer inner.Close()
	client := http.Client{Transport: telemetry.Transport{}}
	handler := httpkit.Observe("outer", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", inner.URL, nil)
		response, err := client.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		_ = response.Body.Close()
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/outer", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 3 {
		t.Fatalf("exported %d spans, want 3", count)
	}
}
