// Package mock provides shared query helpers for domain-owned preview fixtures.
package mock

import (
	"context"
	"fmt"
	"sort"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

// Store executes requests against in-memory records.
type Store struct{ Rows []Record }

func (Store) Query(ctx context.Context, sql string, _ ...any) ([]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch sql {
	case "SELECT TraceId FROM otel.otel_traces LIMIT 0", "SELECT TraceId FROM otel.otel_logs LIMIT 0":
		return []map[string]any{}, nil
	default:
		return nil, fmt.Errorf("mock requires request parameters")
	}
}

func (s Store) Execute(ctx context.Context, request query.Request) (query.Result, error) {
	if err := ctx.Err(); err != nil {
		return query.Result{}, err
	}
	if err := request.Validate(); err != nil {
		return query.Result{}, err
	}
	if request.Kind == "trace-services" || request.Kind == "log-services" {
		request.Filter = query.ServiceFilter(request.Filter)
	}
	records := make([]Record, 0, len(s.Rows))
	for _, record := range s.Rows {
		if matches(record, request) {
			records = append(records, record)
		}
	}
	result := query.Result{}
	switch request.Kind {
	case "trace-services", "log-services":
		result.Data = services(records)
	case "traces-keys", "logs-keys":
		result.Data = attributeKeys(records, request.Filter)
	case "red":
		result.Data = buckets(records, false)
		result.Summary = []map[string]any{summary(records)}
	case "logs-volume":
		result.Data = buckets(records, true)
	case "traces", "logs", "detail":
		result.Data = page(records, request.Filter, request.Kind == "detail")
	default:
		return result, fmt.Errorf("unsupported mock query %q", request.Kind)
	}
	return result, nil
}

func page(records []Record, filter query.Filter, ascending bool) []map[string]any {
	sort.SliceStable(records, func(i, j int) bool {
		if ascending {
			return records[i].Timestamp.Before(records[j].Timestamp)
		}
		return records[i].Timestamp.After(records[j].Timestamp)
	})
	start := min(filter.Offset, len(records))
	// The controller consumes the extra record to determine whether another page exists.
	end := min(start+filter.Limit+1, len(records))
	return responseRows(records[start:end])
}
