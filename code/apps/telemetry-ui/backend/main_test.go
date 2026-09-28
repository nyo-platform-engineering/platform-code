package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHTTPSOriginEnablesTransportSecurity(t *testing.T) {
	cfg := config{
		OAuthConfig: auth.OAuthConfig{Origin: "https://telemetry.example.com"},
		WebDistDir:  t.TempDir(),
		TraceStore:  failingStore{},
		LogStore:    failingStore{},
	}
	handler, err := newHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Header().Get("Strict-Transport-Security") != "max-age=31536000" {
		t.Fatal("HTTPS deployment omitted HSTS")
	}
	if response.Header().Get("Permissions-Policy") == "" {
		t.Fatal("permissions policy omitted")
	}
}

func TestMetaDescribesCapabilities(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if strings.Contains(response.Body.String(), `"tenant"`) {
		t.Fatal("metadata exposed the removed tenant alias")
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
	if body.Actor.OrganizationScope != "local" {
		t.Fatalf("unexpected local organization scope: %q", body.Actor.OrganizationScope)
	}
}

func TestLocalAdminSummaryIsAvailableWithoutControlDatabase(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/summary", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var body struct {
		DatabaseConfigured bool `json:"databaseConfigured"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.DatabaseConfigured {
		t.Fatal("local mode reported a configured control database")
	}
}

func TestLocalDatasourceCatalogHasOneDefaultPerSignal(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-sources", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var body struct {
		Traces []struct{ ID string } `json:"traces"`
		Logs   []struct{ ID string } `json:"logs"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Traces) != 1 || len(body.Logs) != 1 || body.Traces[0].ID != "default" || body.Logs[0].ID != "default" {
		t.Fatalf("unexpected local catalog: %#v", body)
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
	t.Setenv("AUTH_MODE", "local")
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

func TestOAuthNeverFallsBackToLocalIdentity(t *testing.T) {
	if _, err := newHandler(config{AuthMode: "oauth"}, slog.Default()); err == nil {
		t.Fatal("oauth started without session storage")
	}
	t.Setenv("AUTH_MODE", "oauth")
	t.Setenv("AUTH_GRANTS_FILE", t.TempDir()+"/missing.json")
	if _, err := loadConfig(); err == nil {
		t.Fatal("oauth accepted missing grants")
	}
}

// Missing OAuth configuration must never silently enable the local identity.
func TestAuthDefaultsToOAuth(t *testing.T) {
	t.Setenv("AUTH_MODE", "")
	t.Setenv("AUTH_GRANTS_FILE", t.TempDir()+"/missing.json")
	if _, err := loadConfig(); err == nil {
		t.Fatal("default startup bypassed OAuth configuration")
	}
	t.Setenv("AUTH_MODE", "local")
	cfg, err := loadConfig()
	if err != nil || cfg.AuthMode != "local" {
		t.Fatalf("explicit local mode: %v", err)
	}
}
