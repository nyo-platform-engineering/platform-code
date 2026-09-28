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

	querymodel "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

type signalStore struct {
	queries  []string
	args     [][]any
	services []string
	fail     bool
	pingFail bool
	pings    int
	selected []string
}

func (s *signalStore) Query(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	s.queries = append(s.queries, sql)
	s.args = append(s.args, args)
	s.selected = append(s.selected, querymodel.DataSourceFromContext(ctx))
	if s.fail {
		return nil, errors.New("offline")
	}
	rows := make([]map[string]any, 0, len(s.services))
	for _, service := range s.services {
		rows = append(rows, map[string]any{"service": service})
	}
	return rows, nil
}

func TestDatasourceSelectionReachesOnlyTheSelectedSignalStore(t *testing.T) {
	traces, logs := &signalStore{services: []string{"api"}}, &signalStore{services: []string{"worker"}}
	h := splitHandler(t, traces, logs)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1/services?signal=traces&dataSource=archive", nil))
	if out.Code != http.StatusOK || len(traces.queries) != 1 || len(logs.queries) != 0 {
		t.Fatalf("signal-scoped service discovery used the wrong store: %d %s", out.Code, out.Body)
	}
	if len(traces.selected) != 1 || traces.selected[0] != "archive" {
		t.Fatalf("datasource selection was not propagated: %#v", traces.selected)
	}
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
func TestServicesUseSelectedSignalConnection(t *testing.T) {
	traces := &signalStore{services: []string{"api", "shared"}}
	logs := &signalStore{services: []string{"worker", "shared"}}
	h := splitHandler(t, traces, logs)
	missingSignal := httptest.NewRecorder()
	h.ServeHTTP(missingSignal, httptest.NewRequest("GET", "/api/v1/services", nil))
	if missingSignal.Code != 400 || len(traces.queries)+len(logs.queries) != 0 {
		t.Fatal("service discovery must require an explicit signal")
	}
	for _, tc := range []struct {
		signal, name, table string
		active, idle        *signalStore
	}{{"traces", "traces.services", "otel_traces", traces, logs}, {"logs", "logs.services", "otel_logs", logs, traces}} {
		t.Run(tc.signal, func(t *testing.T) {
			out := httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1/services?signal="+tc.signal+"&preview=1", nil))
			var preview struct{ Queries []struct{ Name, SQL string } }
			if err := json.Unmarshal(out.Body.Bytes(), &preview); err != nil {
				t.Fatal(err)
			}
			if out.Code != 200 || len(preview.Queries) != 1 || preview.Queries[0].Name != tc.name || len(tc.active.queries)+len(tc.idle.queries) != 0 {
				t.Fatalf("bad %s preview: %s", tc.signal, out.Body)
			}
			out = httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1/services?signal="+tc.signal, nil))
			if out.Code != 200 || len(tc.active.queries) != 1 || len(tc.idle.queries) != 0 || !strings.Contains(tc.active.queries[0], tc.table) || tc.active.args[0][2] != "local" {
				t.Fatalf("services used wrong connection: %s", out.Body)
			}
			tc.active.queries, tc.active.args = nil, nil
		})
	}
}
func TestEitherBackendOutageFailsReadinessAndServices(t *testing.T) {
	for _, traceFailure := range []bool{false, true} {
		h := splitHandler(t, &signalStore{fail: traceFailure}, &signalStore{fail: !traceFailure})
		signal := "logs"
		if traceFailure {
			signal = "traces"
		}
		for _, path := range []string{"/readyz", "/api/v1/services?signal=" + signal} {
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
