package queryinternal

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestJSONSearchIntegration(t *testing.T) {
	if os.Getenv("CLICKHOUSE_INTEGRATION") != "1" {
		t.Skip("set CLICKHOUSE_INTEGRATION=1")
	}
	store := OpenStore()
	defer store.Close()
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
			f := AttributeFilter{Scope: scope, Key: tc.path, Op: tc.op, Value: tc.value}
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
