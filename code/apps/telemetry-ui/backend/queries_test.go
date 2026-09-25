package main

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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
func TestWhereAlwaysScopesTenantAndBindsValues(t *testing.T) {
	from := time.Now()
	f := queryFilter{From: from, To: from.Add(time.Minute), Services: []string{"x' OR 1=1 --"}, Environment: "local", TraceID: strings.Repeat("a", 32), Severities: []string{"error"}}
	for _, logs := range []bool{false, true} {
		sql, args := f.where("tenant-a", logs)
		if !strings.Contains(sql, "ResourceAttributes['tenant.id'] = ?") || strings.Contains(sql, f.Services[0]) || args[2] != "tenant-a" {
			t.Fatalf("unsafe query: %s %#v", sql, args)
		}
	}
}
func TestGapFillingKeepsMissingLatencyNull(t *testing.T) {
	from := time.Date(2026, 9, 25, 1, 0, 30, 0, time.UTC)
	f := queryFilter{From: from, To: from.Add(2 * time.Minute)}
	rows := fillBuckets(nil, f, false)
	if len(rows) != 3 || rows[0]["partial"] != true || rows[1]["partial"] != false || rows[2]["partial"] != true || rows[1]["p90Ms"] != nil || rows[1]["p95Ms"] != nil || rows[1]["requests"] != 0 {
		t.Fatalf("unexpected buckets: %#v", rows)
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
	for _, kind := range []string{"services", "red", "traces", "detail", "logs-volume", "logs"} {
		t.Run(kind, func(t *testing.T) {
			store := &captureStore{}
			out := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest("GET", "/?traceId="+strings.Repeat("a", 32), nil)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), principalKey, principal{Tenant: "test-scope"}))
			analyticsHandler(store, kind, make(chan struct{}, 1))(c)
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
	analyticsHandler(store, "traces", make(chan struct{}, 1))(c)
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
	sql, args := f.where("tenant-a", true)
	if !strings.Contains(sql, "ServiceName IN (?, ?)") || !strings.Contains(sql, "AND (SeverityNumber BETWEEN ? AND ? OR SeverityNumber BETWEEN ? AND ?)") || args[2] != "tenant-a" || len(args) != 10 {
		t.Fatalf("%s %#v", sql, args)
	}
	sql, _ = f.where("tenant-a", false)
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
		f := queryFilter{Search: term}
		sql, args := f.where("isolated", logs)
		if strings.Contains(sql, term) || args[2] != "isolated" || args[len(args)-1] != term || !strings.Contains(sql, "AND (positionCaseInsensitiveUTF8(") {
			t.Fatalf("unsafe search: %s %#v", sql, args)
		}
		if !strings.Contains(f.settings(), "read_overflow_mode='throw'") {
			t.Fatal("search must fail on resource limits")
		}
	}
	f := queryFilter{Search: strings.Repeat("A", 32)}
	sql, args := f.where("isolated", false)
	if !strings.Contains(sql, "AND TraceId = ?") || strings.Contains(sql, "positionCaseInsensitive") || args[len(args)-1] != strings.Repeat("a", 32) {
		t.Fatal("full trace ID must use exact match")
	}
}
