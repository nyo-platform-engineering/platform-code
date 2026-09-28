package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	logmodel "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	tracemodel "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func TestMockTimeFollowsQueryWindow(t *testing.T) {
	end := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, signal := range []struct {
		name  string
		store query.MockExecutor
	}{
		{"traces", tracemodel.MockStore{}},
		{"logs", logmodel.MockStore{}},
	} {
		t.Run(signal.name, func(t *testing.T) {
			fetch := func(to time.Time, offset int) []map[string]any {
				t.Helper()
				filter := query.Filter{From: to.Add(-5 * time.Minute), To: to, Limit: 2, Offset: offset}
				result, err := signal.store.Execute(context.Background(), query.Request{Filter: filter, Kind: signal.name, Tenant: "local"})
				rows := result.Data
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) == 0 {
					t.Fatal("mock window is empty")
				}
				return rows
			}
			first := fetch(end, 0)
			later := fetch(end.Add(48*time.Hour), 0)
			shift := later[0]["timestamp"].(time.Time).Sub(first[0]["timestamp"].(time.Time))
			if shift != 48*time.Hour {
				t.Fatalf("timestamps shifted by %s", shift)
			}
			if !reflect.DeepEqual(first, fetch(end, 0)) {
				t.Fatal("fixed time window changed")
			}
			page := fetch(end, 2)
			if !reflect.DeepEqual(first[2], page[0]) {
				t.Fatal("pagination changed record timestamps")
			}
		})
	}
}

func TestMockConfiguration(t *testing.T) {
	t.Setenv("MOCK", "")
	cfg, err := loadConfig()
	if err != nil || cfg.Mock {
		t.Fatalf("mock must default off: %+v %v", cfg, err)
	}
	t.Setenv("MOCK", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://unreachable:4318")
	cfg, err = loadConfig()
	if err != nil || !cfg.Mock || cfg.BackendOTLPEnabled {
		t.Fatalf("mock must disable export: %+v %v", cfg, err)
	}
	t.Setenv("MOCK", "typo")
	if _, err = loadConfig(); err == nil {
		t.Fatal("invalid mock flag accepted")
	}
}

func TestMockAPI(t *testing.T) {
	epoch := time.Now().UTC()
	handler, err := newHandler(config{TraceStore: tracemodel.MockStore{}, LogStore: logmodel.MockStore{}, WebDistDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		body := map[string]any{}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/readyz", nil))
	if response.Code != 200 {
		t.Fatal("mock not ready")
	}
	first := get("/api/v1/traces?limit=2")
	rows := first["data"].([]any)
	if len(rows) != 2 || first["truncated"] != true {
		t.Fatalf("bad pagination: %v", first)
	}
	trace := rows[0].(map[string]any)["traceId"].(string)
	detail := get("/api/v1/traces/" + trace)["data"].([]any)
	if len(detail) != 3 {
		t.Fatalf("expected 3 spans, got %v", detail)
	}
	logs := get("/api/v1/logs?traceId=" + trace)["data"].([]any)
	if len(logs) != 3 {
		t.Fatalf("missing correlated logs: %v", logs)
	}
	second := get("/api/v1/traces?limit=2&offset=2")["data"].([]any)
	if second[0].(map[string]any)["traceId"] == trace {
		t.Fatal("pagination repeated a trace")
	}
	errors := get("/api/v1/logs?severity=error")["data"].([]any)
	if len(errors) == 0 {
		t.Fatal("missing errors")
	}
	for _, row := range errors {
		if row.(map[string]any)["severity"] != "error" {
			t.Fatal("severity filter ignored")
		}
	}
	if len(get("/api/v1/traces?service=absent")["data"].([]any)) != 0 {
		t.Fatal("service filter ignored")
	}
	if len(get("/api/v1/logs?environment=production")["data"].([]any)) != 0 {
		t.Fatal("environment filter ignored")
	}
	attr := url.QueryEscape(`{"scope":"body","key":"order.total","op":"gte","value":"150"}`)
	filtered := get("/api/v1/logs?attr=" + attr)["data"].([]any)
	if len(filtered) == 0 {
		t.Fatal("JSON attribute filter returned no rows")
	}
	for _, item := range filtered {
		var body struct {
			Order struct {
				Total int `json:"total"`
			} `json:"order"`
		}
		if err := json.Unmarshal([]byte(item.(map[string]any)["body"].(string)), &body); err != nil || body.Order.Total < 150 {
			t.Fatal("JSON attribute filter ignored")
		}
	}
	result, err := (tracemodel.MockStore{}).Execute(context.Background(), query.Request{Filter: query.Filter{From: epoch.Add(-time.Hour), To: epoch, Limit: 100}, Kind: "traces", Tenant: "another-tenant"})
	isolated := result.Data
	if err != nil || len(isolated) != 0 {
		t.Fatal("mock tenant isolation failed")
	}
	for _, path := range []string{"/api/v1/services", "/api/v1/traces/red", "/api/v1/logs/volume", "/api/v1/traces/attributes", "/api/v1/logs/attributes"} {
		if len(get(path)["data"].([]any)) == 0 {
			t.Fatalf("empty preview: %s", path)
		}
	}
	if len(get("/api/v1/traces?preview=1")["queries"].([]any)) != 1 {
		t.Fatal("SQL preview missing")
	}
}
