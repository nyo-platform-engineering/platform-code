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
)

type signalStore struct {
	queries  []string
	args     [][]any
	services []string
	fail     bool
	pingFail bool
	pings    int
}

func (s *signalStore) Query(_ context.Context, sql string, args ...any) ([]map[string]any, error) {
	s.queries = append(s.queries, sql)
	s.args = append(s.args, args)
	if s.fail {
		return nil, errors.New("offline")
	}
	rows := make([]map[string]any, 0, len(s.services))
	for _, service := range s.services {
		rows = append(rows, map[string]any{"service": service})
	}
	return rows, nil
}
func splitHandler(t *testing.T, traces, logs *signalStore) http.Handler {
	t.Helper()
	h, err := newHandler(config{TraceStore: traces, LogStore: logs, WebDistDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func TestSignalRoutesUseTheirOwnConnection(t *testing.T) {
	for _, path := range []string{"/traces", "/traces/red", "/traces/attributes", "/traces/" + strings.Repeat("a", 32), "/logs", "/logs/volume", "/logs/attributes"} {
		t.Run(path, func(t *testing.T) {
			traces, logs := &signalStore{}, &signalStore{}
			h := splitHandler(t, traces, logs)
			out := httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1"+path, nil))
			if out.Code != 200 {
				t.Fatalf("%d: %s", out.Code, out.Body)
			}
			active, idle := traces, logs
			if strings.HasPrefix(path, "/logs") {
				active, idle = logs, traces
			}
			if len(active.queries) == 0 || len(idle.queries) != 0 {
				t.Fatal("query used wrong connection")
			}
			for _, args := range active.args {
				if args[2] != "local" {
					t.Fatal("tenant lost")
				}
			}
		})
	}
}
func TestServicesMergeAcrossConnectionsAndPreviewBoth(t *testing.T) {
	traces := &signalStore{services: []string{"api", "shared"}}
	logs := &signalStore{services: []string{"worker", "shared"}}
	h := splitHandler(t, traces, logs)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1/services?preview=1", nil))
	var preview struct{ Queries []struct{ Name, SQL string } }
	if err := json.Unmarshal(out.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if out.Code != 200 || len(preview.Queries) != 2 || len(traces.queries)+len(logs.queries) != 0 {
		t.Fatal("preview must describe both queries without executing")
	}
	if preview.Queries[0].Name != "trace-services" || preview.Queries[1].Name != "log-services" {
		t.Fatal("preview lost connection labels")
	}
	out = httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1/services", nil))
	var body struct{ Data []struct{ Service string } }
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if out.Code != 200 || len(body.Data) != 3 || body.Data[0].Service != "api" || body.Data[1].Service != "shared" || body.Data[2].Service != "worker" {
		t.Fatalf("bad merge: %s", out.Body)
	}
	if len(traces.queries) != 1 || len(logs.queries) != 1 || !strings.Contains(traces.queries[0], "FROM otel.otel_traces") || !strings.Contains(logs.queries[0], "FROM otel.otel_logs") {
		t.Fatal("services used wrong connection")
	}
	if traces.args[0][2] != "local" || logs.args[0][2] != "local" {
		t.Fatal("service tenant lost")
	}
}
func TestEitherBackendOutageFailsReadinessAndServices(t *testing.T) {
	for _, traceFailure := range []bool{false, true} {
		h := splitHandler(t, &signalStore{fail: traceFailure}, &signalStore{fail: !traceFailure})
		for _, path := range []string{"/readyz", "/api/v1/services"} {
			out := httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
			if out.Code != 503 {
				t.Fatalf("%s should fail: %d", path, out.Code)
			}
		}
	}
}

func (s *signalStore) Ping(context.Context) error {
	s.pings++
	if s.pingFail {
		return errors.New("connection unavailable")
	}
	return nil
}
func TestReadinessChecksConnectionsInUnifiedAndSplitModes(t *testing.T) {
	for _, unified := range []bool{false, true} {
		traces, logs := &signalStore{}, &signalStore{}
		if unified {
			logs = traces
		}
		h := splitHandler(t, traces, logs)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("GET", "/readyz", nil))
		if out.Code != 200 || traces.pings == 0 || logs.pings == 0 {
			t.Fatal("readiness did not check active connections")
		}
		logs.pingFail = true
		out = httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("GET", "/readyz", nil))
		if out.Code != 503 {
			t.Fatal("failed connection reported ready")
		}
	}
}
