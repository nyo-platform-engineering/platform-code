// Package mock provides shared query helpers for domain-owned preview fixtures.
package mock

import (
	"context"
	"fmt"
	"sort"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

// Store executes validated query plans against in-memory records.
type Store struct{ Rows []Record }

func (Store) Query(ctx context.Context, sql string, _ ...any) ([]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch sql {
	case "SELECT TraceId FROM otel.otel_traces LIMIT 0", "SELECT TraceId FROM otel.otel_logs LIMIT 0":
		return []map[string]any{}, nil
	default:
		return nil, fmt.Errorf("mock requires a typed query plan")
	}
}

func (s Store) Execute(ctx context.Context, plan query.CompiledQuery) ([]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(s.Rows))
	for _, record := range s.Rows {
		if matches(record, plan) {
			records = append(records, record)
		}
	}
	switch plan.Name {
	case "trace-services", "log-services":
		return services(records), nil
	case "traces-keys", "logs-keys":
		return attributeKeys(records, plan.Filter), nil
	case "red-summary":
		return []map[string]any{summary(records)}, nil
	case "red":
		return buckets(records, false), nil
	case "logs-volume":
		return buckets(records, true), nil
	case "traces", "logs", "detail":
		return page(records, plan.Filter, plan.Name == "detail"), nil
	default:
		return nil, fmt.Errorf("unsupported mock query %q", plan.Name)
	}
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
