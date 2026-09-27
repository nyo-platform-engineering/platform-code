package common

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func TestTenantIsolationIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(raw)
	writer := clickhouse.OpenDB(&clickhouse.Options{Addr: []string{"127.0.0.1:9000"}, Auth: clickhouse.Auth{Database: "otel", Username: "otelcollector", Password: "local-clickhouse-otel"}})
	defer writer.Close()
	attrs := map[string]string{"tenant.id": "integration-other", "deployment.environment.name": "local"}
	if _, err := writer.Exec("INSERT INTO otel.otel_traces (Timestamp,TraceId,SpanId,SpanKind,ServiceName,ResourceAttributes,Duration) VALUES (fromUnixTimestamp64Nano(?),?,?,?,?,?,?)", time.Now().UnixNano(), id, "abcdef0123456789", "Server", "integration-test", attrs, uint64(1000000)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec("INSERT INTO otel.otel_logs (Timestamp,TraceId,SpanId,ServiceName,ResourceAttributes,Body,SeverityNumber) VALUES (fromUnixTimestamp64Nano(?),?,?,?,?,?,?)", time.Now().UnixNano(), id, "abcdef0123456789", "integration-test", attrs, "isolated fixture", uint8(9)); err != nil {
		t.Fatal(err)
	}
	store := model.OpenStore()
	defer store.Close()
	for _, kind := range []string{"detail", "logs", "traces", "services"} {
		for _, tenant := range []string{"local", "integration-other"} {
			out := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest("GET", "/?traceId="+id+"&service=missing&service=integration-test&severity=error&severity=info&status=error&status=ok", nil)
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{Tenant: tenant}))
			analyticsHandler(store, kind, make(chan struct{}, 1))(c)
			var body struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tenant == "integration-other" {
				want = 1
			}
			if out.Code != 200 || len(body.Data) != want {
				t.Fatalf("%s tenant %s: %d %s", kind, tenant, out.Code, out.Body)
			}
		}
	}
	// The small fixture expires through the local table TTL; never delete shared telemetry.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Query(cancelled, "SELECT 1"); err == nil {
		t.Fatal("cancelled query succeeded")
	}
}

// The matching records sit beyond the first unfiltered page.
func TestSearchIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(raw)
	tenant := "search-" + id
	writer := clickhouse.OpenDB(&clickhouse.Options{Addr: []string{"127.0.0.1:9000"}, Auth: clickhouse.Auth{Database: "otel", Username: "otelcollector", Password: "local-clickhouse-otel"}})
	defer writer.Close()
	attrs := map[string]string{"tenant.id": tenant, "deployment.environment.name": "local"}
	needle := "Needle%_' literal"
	now := time.Now().UnixNano()
	for _, query := range []string{
		"INSERT INTO otel.otel_traces (Timestamp,TraceId,SpanId,SpanKind,ServiceName,ResourceAttributes,SpanName,Duration) SELECT fromUnixTimestamp64Nano(? - toInt64(number)*1000000000),?,leftPad(hex(number),16,'0'),'Server','search-test',?,if(number>=103,?,'noise'),1000000 FROM numbers(105)",
		"INSERT INTO otel.otel_logs (Timestamp,TraceId,SpanId,ServiceName,ResourceAttributes,Body,SeverityNumber) SELECT fromUnixTimestamp64Nano(? - toInt64(number)*1000000000),?,leftPad(hex(number),16,'0'),'search-test',?,if(number>=103,?,'noise'),9 FROM numbers(105)",
	} {
		if _, err := writer.Exec(query, now, id, attrs, needle); err != nil {
			t.Fatal(err)
		}
	}
	store := model.OpenStore()
	defer store.Close()
	run := func(kind, scope, params string) map[string]any {
		out := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(out)
		c.Request = httptest.NewRequest("GET", "/?"+params, nil)
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{Tenant: scope}))
		analyticsHandler(store, kind, make(chan struct{}, 1))(c)
		if out.Code != 200 {
			t.Fatalf("%s: %d %s", kind, out.Code, out.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	q := "q=" + url.QueryEscape(strings.ToLower(needle))
	for _, kind := range []string{"traces", "logs"} {
		base := run(kind, tenant, "limit=100")
		if len(base["data"].([]any)) != 100 {
			t.Fatal("fixture should fill first page")
		}
		for _, row := range base["data"].([]any) {
			item := row.(map[string]any)
			if item["name"] == needle || item["body"] == needle {
				t.Fatal("match must be beyond first page")
			}
		}
		result := run(kind, tenant, q+"&limit=1")
		if len(result["data"].([]any)) != 1 || result["truncated"] != true || result["nextOffset"] != float64(1) {
			t.Fatalf("wrong search page: %#v", result)
		}
		next := run(kind, tenant, q+"&limit=1&offset=1")
		if len(next["data"].([]any)) != 1 || next["truncated"] != false {
			t.Fatalf("wrong next page: %#v", next)
		}
		if len(run(kind, "unrelated", q)["data"].([]any)) != 0 {
			t.Fatal("search leaked tenant")
		}
		if len(run(kind, tenant, "q="+strings.ToUpper(id))["data"].([]any)) != 100 {
			t.Fatal("exact trace ID search failed")
		}
	}
	red := run("red", tenant, q)
	if red["summary"].(map[string]any)["requests"] != float64(2) {
		t.Fatalf("incorrect RED search summary: %#v", red["summary"])
	}
	logs := run("logs-volume", tenant, q)
	total := float64(0)
	for _, row := range logs["data"].([]any) {
		total += row.(map[string]any)["records"].(float64)
	}
	if total != 2 {
		t.Fatalf("incorrect log chart total %v", total)
	}
}

func TestAttributeSearchIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(raw)
	tenant := "attributes-" + id
	writer := clickhouse.OpenDB(&clickhouse.Options{Addr: []string{"127.0.0.1:9000"}, Auth: clickhouse.Auth{Database: "otel", Username: "otelcollector", Password: "local-clickhouse-otel"}})
	defer writer.Close()
	resource := map[string]string{"tenant.id": tenant, "deployment.environment.name": "local", "region": "ap-southeast-1"}
	for i := 0; i < 60; i++ {
		resource[fmt.Sprintf("discovery.key.%02d", i)] = "test"
	}
	maps := []map[string]string{
		{"code": "500", "method": "GET", "empty": "", "literal.key": "quote%'"},
		{"code": "200", "method": "POST"}, {}, {"code": "oops", "method": "GET"},
	}
	for _, attributes := range maps {
		for _, query := range []string{
			"INSERT INTO otel.otel_traces (Timestamp,TraceId,SpanId,SpanKind,ServiceName,ResourceAttributes,SpanAttributes,Duration) VALUES (fromUnixTimestamp64Nano(?),?,'abcdef0123456789','Server','attribute-test',?,?,1000000)",
			"INSERT INTO otel.otel_logs (Timestamp,TraceId,SpanId,ServiceName,ResourceAttributes,LogAttributes,Body,SeverityNumber) VALUES (fromUnixTimestamp64Nano(?),?,'abcdef0123456789','attribute-test',?,?,'fixture',9)",
		} {
			if _, err := writer.Exec(query, time.Now().UnixNano(), id, resource, attributes); err != nil {
				t.Fatal(err)
			}
		}
	}
	store := model.OpenStore()
	defer store.Close()
	run := func(kind, scope string, conditions []model.AttributeFilter, extra string) map[string]any {
		params := url.Values{}
		for _, condition := range conditions {
			encoded, _ := json.Marshal(condition)
			params.Add("attr", string(encoded))
		}
		out := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(out)
		c.Request = httptest.NewRequest("GET", "/?"+params.Encode()+extra, nil)
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{Tenant: scope}))
		analyticsHandler(store, kind, make(chan struct{}, 1))(c)
		if out.Code != 200 {
			t.Fatalf("%s %d %s", kind, out.Code, out.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	for _, scope := range []string{"span", "log"} {
		kind := "traces"
		if scope == "log" {
			kind = "logs"
		}
		keys := run(kind+"-keys", tenant, nil, "&scope="+scope+"&keySearch=CO")
		if len(keys["data"].([]any)) != 1 || keys["data"].([]any)[0].(map[string]any)["key"] != "code" {
			t.Fatalf("wrong discovered keys: %#v", keys)
		}
		if len(run(kind+"-keys", "unrelated", nil, "&scope="+scope)["data"].([]any)) != 0 {
			t.Fatal("key discovery leaked tenant")
		}
		if len(run(kind+"-keys", tenant, nil, "&scope="+scope+"&service=not-present")["data"].([]any)) != 0 {
			t.Fatal("key discovery ignored service")
		}
		capped := run(kind+"-keys", tenant, nil, "&scope=resource&keySearch=discovery")
		if len(capped["data"].([]any)) != 50 || capped["truncated"] != true || capped["nextOffset"] != nil {
			t.Fatalf("discovery cap failed: %#v", capped)
		}
		narrowed := run(kind+"-keys", tenant, nil, "&scope=resource&keySearch=discovery.key.59")
		if len(narrowed["data"].([]any)) != 1 {
			t.Fatal("cannot find key beyond first 50")
		}
		for _, tc := range []struct {
			key, op, value string
			want           int
		}{
			{"code", "eq", "500", 1}, {"method", "neq", "GET", 1},
			{"method", "contains", "get", 2}, {"empty", "eq", "", 1},
			{"empty", "exists", "", 1}, {"empty", "missing", "", 3},
			{"code", "gte", "500", 1}, {"code", "gt", "200", 1},
			{"code", "lte", "200", 1}, {"code", "lt", "500", 1},
			{"literal.key", "eq", "quote%'", 1},
			{"x'] OR 1=1 --", "eq", "quote%'", 0},
		} {
			result := run(kind, tenant, []model.AttributeFilter{{Scope: scope, Key: tc.key, Op: tc.op, Value: tc.value}}, "")
			if len(result["data"].([]any)) != tc.want {
				t.Fatalf("%s %s: %#v", scope, tc.op, result)
			}
		}
		conditions := []model.AttributeFilter{{Scope: "resource", Key: "region", Op: "eq", Value: "ap-southeast-1"}, {Scope: scope, Key: "code", Op: "gte", Value: "500"}}
		if len(run(kind, tenant, conditions, "")["data"].([]any)) != 1 {
			t.Fatal("AND failed")
		}
		if len(run(kind, "unrelated", conditions, "")["data"].([]any)) != 0 {
			t.Fatal("tenant leaked")
		}
		if scope == "span" {
			if run("red", tenant, conditions, "")["summary"].(map[string]any)["requests"] != float64(1) {
				t.Fatal("wrong RED total")
			}
		} else {
			sum := float64(0)
			for _, row := range run("logs-volume", tenant, conditions, "")["data"].([]any) {
				sum += row.(map[string]any)["records"].(float64)
			}
			if sum != 1 {
				t.Fatal("wrong log chart total")
			}
		}
		page := run(kind, tenant, []model.AttributeFilter{{Scope: scope, Key: "method", Op: "exists"}}, "&limit=1&offset=1")
		if len(page["data"].([]any)) != 1 || page["truncated"] != true {
			t.Fatal("attribute pagination failed")
		}
	}
}
