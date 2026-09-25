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
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := newHandler(config{WebDistDir: t.TempDir(), Store: failingStore{}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		Capabilities []capability `json:"capabilities"`
		Actor        principal    `json:"actor"`
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
