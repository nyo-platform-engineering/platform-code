package model

import (
	"fmt"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

const REDFields = "count() AS requests, countIf(StatusCode = 'Error') AS errors, if(count() = 0, 0, countIf(StatusCode = 'Error') / count()) AS errorRate, if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[1]/1000000) AS p50Ms, if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[2]/1000000) AS p90Ms, if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[3]/1000000) AS p95Ms, if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[4]/1000000) AS p99Ms"

func CompileQueries(f query.Filter, kind, tenant string) ([]query.CompiledQuery, bool, error) {
	if err := f.ValidateScope(tenant, "span", kind == "traces-keys"); err != nil {
		return nil, false, err
	}
	columns, group, order := "", "", ""
	list, serverOnly, discovery := false, false, false
	switch kind {
	case "traces":
		columns = "Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, ServiceName AS service, SpanName AS name, Duration/1000000 AS durationMs, StatusCode AS status"
		order = "Timestamp DESC, TraceId, SpanId"
		list = true
		serverOnly = true
	case "detail":
		columns = "Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, ParentSpanId AS parentSpanId, ServiceName AS service, SpanName AS name, Duration/1000000 AS durationMs, StatusCode AS status, StatusMessage AS message, SpanAttributes AS attributes"
		order = "Timestamp, SpanId"
		list = true
	case "red":
		columns = "toStartOfMinute(Timestamp) AS bucket, " + REDFields
		group = "bucket"
		order = "bucket"
		serverOnly = true
	case "traces-keys":
		column := "ResourceAttributes"
		if f.DiscoveryScope == "span" {
			column = "SpanAttributes"
		}
		columns = "DISTINCT arrayJoin(mapKeys(" + column + ")) AS key"
		order = "key"
		discovery = true
		serverOnly = true
	default:
		return nil, false, fmt.Errorf("unsupported traces query kind: %q", kind)
	}
	builder, err := query.Select(query.Traces, columns, f, tenant, serverOnly)
	if err != nil {
		return nil, false, err
	}
	builder.GroupBy(group).OrderBy(order)
	if list {
		builder.Page(f.Limit+1, f.Offset)
	}
	if discovery {
		builder.ContainsKey(f.KeySearch).Page(51, 0).SearchBudget()
	}
	compiled, err := builder.Compile(kind)
	if err != nil {
		return nil, false, err
	}
	result := []query.CompiledQuery{compiled}
	if kind == "red" {
		summary, err := query.Select(query.Traces, REDFields, f, tenant, true)
		if err != nil {
			return nil, false, err
		}
		compiled, err := summary.Compile("red-summary")
		if err != nil {
			return nil, false, err
		}
		result = append(result, compiled)
	}
	return result, list, nil
}
