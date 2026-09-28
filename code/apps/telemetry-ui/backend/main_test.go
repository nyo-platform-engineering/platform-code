package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	view "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/view"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := newHandler(config{WebDistDir: t.TempDir(), TraceStore: failingStore{}, LogStore: failingStore{}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestHealthIsPublic(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestMetaDescribesCapabilities(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var body struct {
		Capabilities []view.Capability `json:"capabilities"`
		Actor        auth.Principal    `json:"actor"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Capabilities) != 2 || body.Capabilities[0].Bucket != "1m" {
		t.Fatalf("unexpected capabilities: %#v", body.Capabilities)
	}
	if body.Actor.Tenant != "local" {
		t.Fatalf("unexpected local tenant: %q", body.Actor.Tenant)
	}
}

func TestStorageOutageIsExplicit(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/traces/red", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func TestPublicOTLPEndpointDoesNotExist(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/otlp/v1/traces", nil)
	request.Header.Set("Content-Type", "application/x-protobuf")
	testHandler(t).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

type failingStore struct{}

func (failingStore) Query(context.Context, string, ...any) ([]map[string]any, error) {
	return nil, errors.New("offline")
}

func TestMaxConcurrentQueriesConfig(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{{"", 4}, {"1", 1}, {"8", 8}, {"0", 0}, {"-1", 0}, {"abc", 0}, {"1.5", 0}} {
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv("MAX_CONCURRENT_QUERIES", tc.value)
			cfg, err := loadConfig()
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid limit accepted")
				}
				return
			}
			if err != nil || cfg.MaxConcurrentQueries != tc.want {
				t.Fatalf("limit = %d, error = %v; want %d", cfg.MaxConcurrentQueries, err, tc.want)
			}
		})
	}
}
