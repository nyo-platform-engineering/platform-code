package model

import (
	"context"
	"fmt"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	"sort"
)

func CompileQueries(f query.Filter, kind, tenant string) ([]query.CompiledQuery, bool, error) {
	if kind != "services" {
		return nil, false, fmt.Errorf("unsupported metadata query kind: %q", kind)
	}
	compiled, err := query.Services(f, tenant)
	if err != nil {
		return nil, false, err
	}
	return compiled, false, nil
}

// ExecuteServices queries both backends with the same deadline and trusted tenant.
// Any failure rejects the whole response rather than hiding a signal's services.
func ExecuteServices(ctx context.Context, traces, logs query.QueryStore, queries []query.CompiledQuery) ([]map[string]any, error) {
	if len(queries) != 2 {
		return nil, fmt.Errorf("expected two service queries")
	}
	names := map[string]struct{}{}
	for n, store := range []query.QueryStore{traces, logs} {
		rows, err := store.Query(ctx, queries[n].SQL, queries[n].Args...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if name, ok := row["service"].(string); ok {
				names[name] = struct{}{}
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	if len(sorted) > 500 {
		sorted = sorted[:500]
	}
	result := make([]map[string]any, 0, len(sorted))
	for _, name := range sorted {
		result = append(result, map[string]any{"service": name})
	}
	return result, nil
}
