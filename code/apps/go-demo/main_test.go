package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPContract(t *testing.T) {
	handler := newHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/readyz", http.StatusOK},
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if test.path == "/" && test.method == http.MethodGet {
				if response.Header().Get("Content-Type") != "application/json" {
					t.Fatal("expected a JSON response")
				}
				var body map[string]string
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["service"] != "go-demo" || body["message"] == "" {
					t.Fatalf("unexpected response: %v", body)
				}
			}
		})
	}
}

func TestInvalidPort(t *testing.T) {
	for _, port := range []string{"invalid", "0", "65536"} {
		t.Run(port, func(t *testing.T) {
			t.Setenv("PORT", port)
			if err := run(context.Background(), slog.Default()); err == nil {
				t.Fatal("expected invalid PORT to fail before starting the server")
			}
		})
	}
}
