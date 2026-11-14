package common

import (
	"fmt"
	"time"

	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func FillBuckets(rows []map[string]any, f model.Filter, logs bool) []map[string]any {
	index := map[string]map[string]any{}
	for _, row := range rows {
		if bucket, ok := row["bucket"].(time.Time); ok {
			index[bucket.UTC().Format(time.RFC3339)+fmt.Sprint(row["severity"])] = row
		}
	}
	result := make([]map[string]any, 0)
	for bucket := f.From.UTC().Truncate(time.Minute); bucket.Before(f.To); bucket = bucket.Add(time.Minute) {
		bands := []string{""}
		if logs {
			bands = []string{"unspecified", "trace", "debug", "info", "warn", "error", "fatal"}
		}
		for _, band := range bands {
			key := bucket.Format(time.RFC3339) + "<nil>"
			if logs {
				key = bucket.Format(time.RFC3339) + band
			}
			row := index[key]
			if row == nil {
				if logs {
					row = map[string]any{"bucket": bucket, "severity": band, "records": 0}
				} else {
					row = map[string]any{"bucket": bucket, "requests": 0, "errors": 0, "errorRate": 0, "p50Ms": nil, "p90Ms": nil, "p95Ms": nil, "p99Ms": nil}
				}
			}
			row["partial"] = bucket.Before(f.From) || bucket.Add(time.Minute).After(f.To)
			result = append(result, row)
		}
	}
	return result
}
func PreviewQueries(queries []model.CompiledQuery) []map[string]any {
	result := make([]map[string]any, 0, len(queries))
	for _, query := range queries {
		params := make([]map[string]any, 0, len(query.Args))
		for i, value := range query.Args {
			// Strings preserve exact Int64 nanoseconds in browsers (beyond Number precision).
			params = append(params, map[string]any{"position": i + 1, "type": fmt.Sprintf("%T", value), "value": fmt.Sprint(value)})
		}
		result = append(result, map[string]any{"name": query.Name, "sql": query.SQL, "parameters": params})
	}
	return result
}
