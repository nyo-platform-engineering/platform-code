package model

import (
	"fmt"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

const SeveritySQL = "multiIf(SeverityNumber>=21,'fatal',SeverityNumber>=17,'error',SeverityNumber>=13,'warn',SeverityNumber>=9,'info',SeverityNumber>=5,'debug',SeverityNumber>=1,'trace','unspecified')"

func CompileQueries(f query.Filter, kind, tenant string) ([]query.CompiledQuery, bool, error) {
	if err := f.ValidateScope(tenant, "log", kind == "logs-keys"); err != nil {
		return nil, false, err
	}
	columns, group, order := "", "", ""
	list, serverOnly, discovery := false, false, false
	switch kind {
	case "logs":
		columns = "Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, ServiceName AS service, " + SeveritySQL + " AS severity, Body AS body, LogAttributes AS attributes, ResourceAttributes AS resourceAttributes"
		order = "Timestamp DESC, TraceId, SpanId, Body"
		list = true
	case "logs-volume":
		columns = "toStartOfMinute(Timestamp) AS bucket, " + SeveritySQL + " AS severity, count() AS records"
		group = "bucket,severity"
		order = "bucket,severity"
	case "logs-keys":
		column := "ResourceAttributes"
		if f.DiscoveryScope == "log" {
			column = "LogAttributes"
		}
		columns = "DISTINCT arrayJoin(mapKeys(" + column + ")) AS key"
		order = "key"
		discovery = true
		serverOnly = false
	default:
		return nil, false, fmt.Errorf("unsupported logs query kind: %q", kind)
	}
	builder, err := query.Select(query.Logs, columns, f, tenant, serverOnly)
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
	return result, list, nil
}
