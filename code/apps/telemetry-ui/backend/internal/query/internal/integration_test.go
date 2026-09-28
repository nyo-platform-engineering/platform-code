package queryinternal

import (
	"context"
	"encoding/json"
	"fmt"
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
	store := OpenStore()
	defer store.Close()
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
	if _, err := store.Query(context.Background(), "INSERT INTO otel.otel_traces (TraceId) SELECT 'permission-check' WHERE 0"); err == nil {
		t.Fatal("read-only user unexpectedly permitted INSERT")
	}
}

func TestQueryPlanIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1")
	}
	store := OpenStore()
	defer store.Close()
	f := Filter{From: time.Now().Add(-time.Hour), To: time.Now(), Services: []string{"go-demo"}, Limit: 100, Attributes: []AttributeFilter{{Scope: "body", Key: "user.id", Op: "eq", Value: "42"}}}
	queries, compileErr := Compile(Request{Filter: f, Signal: SignalLogs, Operation: OperationRecords, OrganizationScope: "local"})
	if compileErr != nil {
		t.Fatal(compileErr)
	}
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
	queries, compileErr = Compile(Request{Filter: f, Signal: SignalTraces, Operation: OperationMetrics, OrganizationScope: "local"})
	if compileErr != nil {
		t.Fatal(compileErr)
	}
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
	store := OpenStore()
	defer store.Close()
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
