package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClickHouseIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1 with Compose running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := openStore()
	defer store.db.Close()
	if _, err := store.Query(ctx, "SELECT TraceId FROM otel.otel_traces LIMIT 0"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"SELECT " + redFields + " FROM otel.otel_traces WHERE ResourceAttributes['tenant.id'] = ? AND SpanKind = 'Server'",
		"SELECT " + severitySQL + " AS severity,count() FROM otel.otel_logs WHERE ResourceAttributes['tenant.id'] = ? GROUP BY severity",
	} {
		if _, err := store.Query(ctx, query, "local"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec("INSERT INTO otel.otel_traces (TraceId) SELECT 'permission-check' WHERE 0"); err == nil {
		t.Fatal("read-only user unexpectedly permitted INSERT")
	}
}

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
	store := openStore()
	defer store.db.Close()
	for _, kind := range []string{"detail", "logs", "traces", "services"} {
		for _, tenant := range []string{"local", "integration-other"} {
			out := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest("GET", "/?traceId="+id+"&service=missing&service=integration-test&severity=error&severity=info&status=error&status=ok", nil)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), principalKey, principal{Tenant: tenant}))
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
	store := openStore()
	defer store.db.Close()
	run := func(kind, scope, params string) map[string]any {
		out := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(out)
		c.Request = httptest.NewRequest("GET", "/?"+params, nil)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), principalKey, principal{Tenant: scope}))
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
	store := openStore()
	defer store.db.Close()
	run := func(kind, scope string, conditions []attributeFilter, extra string) map[string]any {
		params := url.Values{}
		for _, condition := range conditions {
			encoded, _ := json.Marshal(condition)
			params.Add("attr", string(encoded))
		}
		out := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(out)
		c.Request = httptest.NewRequest("GET", "/?"+params.Encode()+extra, nil)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), principalKey, principal{Tenant: scope}))
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
			result := run(kind, tenant, []attributeFilter{{Scope: scope, Key: tc.key, Op: tc.op, Value: tc.value}}, "")
			if len(result["data"].([]any)) != tc.want {
				t.Fatalf("%s %s: %#v", scope, tc.op, result)
			}
		}
		conditions := []attributeFilter{{Scope: "resource", Key: "region", Op: "eq", Value: "ap-southeast-1"}, {Scope: scope, Key: "code", Op: "gte", Value: "500"}}
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
		page := run(kind, tenant, []attributeFilter{{Scope: scope, Key: "method", Op: "exists"}}, "&limit=1&offset=1")
		if len(page["data"].([]any)) != 1 || page["truncated"] != true {
			t.Fatal("attribute pagination failed")
		}
	}
}

func TestJSONSearchIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1")
	}
	store := openStore()
	defer store.db.Close()
	fixtures := []string{`{"user":{"name":"Ada","age":42,"active":true,"empty":"","nil":null},"items":[{"price":12.5}]}`, `{"user":{"name":"Bob","age":"20"}}`, `{}`, `not JSON`}
	for _, scope := range []string{"body", "log", "span", "resource"} {
		for _, tc := range []struct {
			path, op, value string
			want            float64
		}{
			{"user.name", "eq", "Ada", 1}, {"user.name", "neq", "Ada", 1}, {"user.name", "contains", "AD", 1},
			{"user.age", "gt", "30", 1}, {"user.age", "gte", "42", 1}, {"user.age", "lt", "30", 1}, {"user.age", "lte", "20", 1},
			{"user.empty", "eq", "", 1}, {"user.empty", "exists", "", 1}, {"user.empty", "missing", "", 2},
			{"user.nil", "eq", "null", 1}, {"user.nil", "exists", "", 1}, {"user.active", "eq", "true", 1},
			{"items.0.price", "gte", "12.5", 1}, {"items.1.price", "exists", "", 0},
			{"user.name' OR 1=1 --", "exists", "", 0},
		} {
			f := attributeFilter{Scope: scope, Key: tc.path, Op: tc.op, Value: tc.value}
			if scope != "body" {
				f.Key = "payload"
				f.Path = tc.path
			}
			predicate, args := f.sql()
			query := `SELECT count() AS matches FROM (SELECT arrayJoin(?) AS Body, map('payload',Body) AS LogAttributes, LogAttributes AS SpanAttributes, LogAttributes AS ResourceAttributes) WHERE ` + predicate
			rows, err := store.Query(context.Background(), query, append([]any{fixtures}, args...)...)
			if err != nil {
				t.Fatalf("%s %s: %v", scope, tc.path, err)
			}
			encoded, _ := json.Marshal(rows)
			var data []map[string]any
			_ = json.Unmarshal(encoded, &data)
			if len(data) != 1 || data[0]["matches"] != tc.want {
				t.Fatalf("%s %s %s: %s", scope, tc.path, tc.op, encoded)
			}
		}
	}
}

func TestQueryPlanIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1")
	}
	store := openStore()
	defer store.db.Close()
	f := queryFilter{From: time.Now().Add(-time.Hour), To: time.Now(), Services: []string{"go-demo"}, Limit: 100, Attributes: []attributeFilter{{Scope: "body", Key: "user.id", Op: "eq", Value: "42"}}}
	queries, _ := compileQueries(f, "logs", "local")
	q := queries[0]
	rows, err := store.Query(context.Background(), "EXPLAIN indexes = 1, actions = 1 "+q.SQL, q.Args...)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rows)
	if !strings.Contains(string(raw), "Prewhere") {
		t.Fatalf("missing PREWHERE stage: %s", raw)
	}
	extractions := 0
	for _, row := range rows {
		if strings.Contains(fmt.Sprint(row["explain"]), "FUNCTION JSONExtractRaw(") {
			extractions++
		}
	}
	if extractions != 1 {
		t.Fatalf("expected one shared JSON extraction, got %d", extractions)
	}
	if !strings.Contains(string(raw), "toStartOfFiveMinutes(Timestamp)") {
		t.Fatal("missing leading log index key")
	}
	t.Log("Verified PREWHERE, leading log time key, and one shared JSON extraction")
	f.Attributes = nil
	queries, _ = compileQueries(f, "red", "local")
	rows, err = store.Query(context.Background(), "EXPLAIN actions = 1 "+queries[0].SQL, queries[0].Args...)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(rows)
	if !strings.Contains(string(raw), "quantilesTDigest") {
		t.Fatal("missing shared percentile aggregate")
	}
}

func TestLatencyPercentilesIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1")
	}
	store := openStore()
	defer store.db.Close()
	rows, err := store.Query(context.Background(), "SELECT "+redFields+" FROM (SELECT (number+1)*1000000 AS Duration, 'Ok' AS StatusCode FROM numbers(1000))")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(rows)
	var data []map[string]float64
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"p50Ms": 500, "p90Ms": 900, "p95Ms": 950, "p99Ms": 990} {
		got := data[0][key]
		if got < want-3 || got > want+3 {
			t.Fatalf("%s = %v, want approximately %v", key, got, want)
		}
	}
}
