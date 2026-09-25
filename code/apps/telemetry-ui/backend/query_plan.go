package main

import (
	"fmt"
	"strings"
	"time"
)

type compiledQuery struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
	Args []any  `json:"-"`
}

// PREWHERE reads scope/filter columns before loading bodies and evaluating JSON.
func (f queryFilter) queryConditions(tenant string, logs, serverOnly bool) (string, []any) {
	base := f
	base.Attributes = nil
	if !traceIDPattern.MatchString(strings.ToLower(base.Search)) {
		base.Search = ""
	}
	pre, args := base.where(tenant, logs)
	full, allArgs := f.where(tenant, logs)
	suffix := strings.TrimPrefix(full, pre)
	baseArgCount := len(args)
	if logs {
		// Match the log table's leading sorting key, retaining exact nanosecond bounds above.
		// To is exclusive: subtract 1 ns before rounding so an aligned end excludes its next bucket.
		pre += " AND toStartOfFiveMinutes(Timestamp) >= toStartOfFiveMinutes(fromUnixTimestamp64Nano(?)) AND toStartOfFiveMinutes(Timestamp) <= toStartOfFiveMinutes(fromUnixTimestamp64Nano(?))"
		args = append(args, f.From.UnixNano(), f.To.Add(-time.Nanosecond).UnixNano())
	}
	if serverOnly {
		pre += " AND SpanKind = 'Server'"
	}
	clause := "PREWHERE " + pre
	if suffix != "" {
		clause += " WHERE " + strings.TrimPrefix(suffix, " AND ")
	}
	return clause, append(args, allArgs[baseArgCount:]...)
}

// Execution and preview share this compiler, including RED's separate summary.
func compileQueries(f queryFilter, kind, tenant string) ([]compiledQuery, bool) {
	if kind == "services" {
		f.Search = ""
		f.Attributes = nil
		f.Statuses = nil
		f.MinDuration = 0
		f.Severities = nil
	}
	where, args := f.queryConditions(tenant, strings.HasPrefix(kind, "logs"), kind == "traces" || kind == "red" || kind == "traces-keys")
	query := ""
	list := false
	settings := f.settings()
	switch kind {
	case "logs-keys", "traces-keys":
		column := map[string]string{"resource": "ResourceAttributes", "span": "SpanAttributes", "log": "LogAttributes"}[f.DiscoveryScope]
		table := "otel.otel_traces"
		if kind == "logs-keys" {
			table = "otel.otel_logs"
		}
		query = "SELECT DISTINCT arrayJoin(mapKeys(" + column + ")) AS key FROM " + table + " " + where
		if f.KeySearch != "" {
			query += " WHERE positionCaseInsensitiveUTF8(key, ?) > 0"
			args = append(args, f.KeySearch)
		}
		query += " ORDER BY key LIMIT 51"
		settings = searchSettings

	case "services":
		query = "SELECT DISTINCT service FROM (SELECT ServiceName AS service FROM otel.otel_traces " + where + " UNION ALL SELECT ServiceName AS service FROM otel.otel_logs " + where + ") ORDER BY service LIMIT 500"
		args = append(args, args...)
	case "red":
		query = "SELECT toStartOfMinute(Timestamp) AS bucket, " + redFields + " FROM otel.otel_traces " + where + " GROUP BY bucket ORDER BY bucket"
	case "traces":
		query = "SELECT Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, ServiceName AS service, SpanName AS name, Duration/1000000 AS durationMs, StatusCode AS status FROM otel.otel_traces " + where + " ORDER BY Timestamp DESC, TraceId, SpanId"
		list = true
	case "detail":
		query = "SELECT Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, ParentSpanId AS parentSpanId, ServiceName AS service, SpanName AS name, Duration/1000000 AS durationMs, StatusCode AS status, StatusMessage AS message, SpanAttributes AS attributes FROM otel.otel_traces " + where + " ORDER BY Timestamp, SpanId"
		list = true
	case "logs-volume":
		query = "SELECT toStartOfMinute(Timestamp) AS bucket, " + severitySQL + " AS severity, count() AS records FROM otel.otel_logs " + where + " GROUP BY bucket,severity ORDER BY bucket,severity"
	case "logs":
		query = "SELECT Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, ServiceName AS service, " + severitySQL + " AS severity, Body AS body, LogAttributes AS attributes, ResourceAttributes AS resourceAttributes FROM otel.otel_logs " + where + " ORDER BY Timestamp DESC, TraceId, SpanId, Body"
		list = true
	}
	if list {
		query += " LIMIT ? OFFSET ?"
		args = append(args, f.Limit+1, f.Offset)
	}

	result := []compiledQuery{{Name: kind, SQL: query + settings, Args: args}}
	if kind == "red" {
		clause, values := f.queryConditions(tenant, false, true)
		result = append(result, compiledQuery{Name: "red-summary", SQL: "SELECT " + redFields + " FROM otel.otel_traces " + clause + f.settings(), Args: values})
	}
	return result, list
}

func previewQueries(queries []compiledQuery) []map[string]any {
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
