package model

import (
	"fmt"
	"sort"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func CompileQueries(request query.Request) ([]query.CompiledQuery, error) {
	switch request.Kind {
	case "services", "trace-services", "log-services":
		return query.Compile(request)
	default:
		return nil, fmt.Errorf("unsupported metadata query kind: %q", request.Kind)
	}
}

// MergeServices combines both signal results into a bounded, sorted list.
func MergeServices(signals ...[]map[string]any) []map[string]any {
	names := map[string]struct{}{}
	for _, rows := range signals {
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
	return result
}
