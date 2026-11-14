package common

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func TestFilterRejectsUnboundedAndInvalidQueries(t *testing.T) {
	for _, query := range []string{"limit=501", "limit=0", "offset=5001", "from=bad", "from=2020-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", "traceId=abc", "traceId=00000000000000000000000000000000", "severity=unknown", "status=broken", "minDurationMs=NaN", "minDurationMs=Inf", "minDurationMs=-1"} {
		t.Run(query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/?"+query, nil)
			if _, err := parseFilter(c); err == nil {
				t.Fatal("expected invalid query")
			}
		})
	}
}

type captureStore struct {
	queries []string
	args    [][]any
}

func (s *captureStore) Query(_ context.Context, q string, args ...any) ([]map[string]any, error) {
	s.queries = append(s.queries, q)
	s.args = append(s.args, args)
	return []map[string]any{}, nil
}
func TestEveryQueryIncludesTenant(t *testing.T) {
	for _, request := range []struct {
		name      string
		signal    model.Signal
		operation model.Operation
	}{
		{"trace services", model.SignalTraces, model.OperationServices},
		{"trace metrics", model.SignalTraces, model.OperationMetrics},
		{"trace records", model.SignalTraces, model.OperationRecords},
		{"trace detail", model.SignalTraces, model.OperationDetail},
		{"log metrics", model.SignalLogs, model.OperationMetrics},
		{"log records", model.SignalLogs, model.OperationRecords},
	} {
		t.Run(request.name, func(t *testing.T) {
			store := &captureStore{}
			out := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest("GET", "/?traceId="+strings.Repeat("a", 32), nil)
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{OrganizationScope: "test-scope"}))
			analyticsHandler(store, request.signal, request.operation, make(chan struct{}, 1))(c)
			if out.Code != 200 {
				t.Fatalf("%d %s", out.Code, out.Body)
			}
			for i, q := range store.queries {
				if !strings.Contains(q, "ResourceAttributes['tenant.id'] = ?") || store.args[i][2] != "test-scope" {
					t.Fatalf("missing tenant: %s", q)
				}
			}
		})
	}
}
func TestMissingPrincipalCannotQuery(t *testing.T) {
	store := &captureStore{}
	out := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(out)
	c.Request = httptest.NewRequest("GET", "/", nil)
	analyticsHandler(store, model.SignalTraces, model.OperationRecords, make(chan struct{}, 1))(c)
	if out.Code != 403 || len(store.queries) != 0 {
		t.Fatal("missing scope must fail closed")
	}
}

// Repeated values are ORed within a filter and ANDed with the tenant boundary.
func TestMultiFilters(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?service=api&service=worker&severity=warn&severity=error&status=ok&status=error", nil)
	f, err := parseFilter(c)
	if err != nil {
		t.Fatal(err)
	}
	sql, args := f.Where("tenant-a", true)
	if !strings.Contains(sql, "ServiceName IN (?, ?)") || !strings.Contains(sql, "AND (SeverityNumber BETWEEN ? AND ? OR SeverityNumber BETWEEN ? AND ?)") || args[2] != "tenant-a" || len(args) != 10 {
		t.Fatalf("%s %#v", sql, args)
	}
	sql, _ = f.Where("tenant-a", false)
	if strings.Contains(sql, "StatusCode") {
		t.Fatal("both statuses should include all traces")
	}
	for _, query := range []string{"service=&service=api", strings.Repeat("service=x&", 21), "severity=info&severity=invalid", "status=ok&status=invalid"} {
		c.Request = httptest.NewRequest("GET", "/?"+query, nil)
		if _, err := parseFilter(c); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
}

func TestSearchValidationAndBinding(t *testing.T) {
	for _, value := range []string{"ab", strings.Repeat("a", 257), "hello\nworld"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/?q="+url.QueryEscape(value), nil)
		if _, err := parseFilter(c); err == nil {
			t.Fatalf("accepted invalid search %q", value)
		}
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?q=abc&q=def", nil)
	if _, err := parseFilter(c); err == nil {
		t.Fatal("accepted repeated q")
	}
	term := "needle%' OR 1=1 --"
	for _, logs := range []bool{false, true} {
		f := model.Filter{Search: term}
		sql, args := f.Where("isolated", logs)
		if strings.Contains(sql, term) || args[2] != "isolated" || args[len(args)-1] != term || !strings.Contains(sql, "AND (positionCaseInsensitiveUTF8(") {
			t.Fatalf("unsafe search: %s %#v", sql, args)
		}
		if !strings.Contains(f.Settings(), "read_overflow_mode='throw'") {
			t.Fatal("search must fail on resource limits")
		}
	}
	f := model.Filter{Search: strings.Repeat("A", 32)}
	sql, args := f.Where("isolated", false)
	if !strings.Contains(sql, "AND TraceId = ?") || strings.Contains(sql, "positionCaseInsensitive") || args[len(args)-1] != strings.Repeat("a", 32) {
		t.Fatal("full trace ID must use exact match")
	}
}
