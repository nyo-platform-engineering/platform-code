package mock

import (
	"sort"
	"time"
)

type bucketKey struct {
	minute   time.Time
	severity string
}

func buckets(records []Record, logs bool) []map[string]any {
	groups := make(map[bucketKey][]Record)
	for _, record := range records {
		key := bucketKey{minute: record.Timestamp.Truncate(time.Minute)}
		if logs {
			key.severity = record.Severity
		}
		groups[key] = append(groups[key], record)
	}
	keys := make([]bucketKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].minute.Equal(keys[j].minute) {
			return keys[i].severity < keys[j].severity
		}
		return keys[i].minute.Before(keys[j].minute)
	})
	rows := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		var row map[string]any
		if logs {
			row = map[string]any{"severity": key.severity, "records": len(groups[key])}
		} else {
			row = summary(groups[key])
		}
		row["bucket"] = key.minute
		rows = append(rows, row)
	}
	return rows
}

func summary(rows []Record) map[string]any {
	result := map[string]any{
		"requests":  len(rows),
		"errors":    0,
		"errorRate": 0.0,
		"p50Ms":     nil,
		"p90Ms":     nil,
		"p95Ms":     nil,
		"p99Ms":     nil,
	}
	if len(rows) == 0 {
		return result
	}
	durations := make([]float64, 0, len(rows))
	errors := 0
	for _, row := range rows {
		durations = append(durations, row.DurationMs)
		if row.Status == "Error" {
			errors++
		}
	}
	sort.Float64s(durations)
	result["errors"] = errors
	result["errorRate"] = float64(errors) / float64(len(rows))
	for key, p := range map[string]float64{"p50Ms": .50, "p90Ms": .90, "p95Ms": .95, "p99Ms": .99} {
		result[key] = percentile(durations, p)
	}
	return result
}

// percentile linearly interpolates sorted samples instead of rounding both tail
// percentiles down to the same sample. Empty windows are handled by summary.
func percentile(sorted []float64, fraction float64) float64 {
	position := float64(len(sorted)-1) * fraction
	lower := int(position)
	upper := min(lower+1, len(sorted)-1)
	weight := position - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}
