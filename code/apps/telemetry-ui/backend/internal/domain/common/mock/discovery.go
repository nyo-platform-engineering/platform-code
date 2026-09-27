package mock

import (
	"sort"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func services(records []Record) []map[string]any {
	names := make(map[string]bool)
	for _, record := range records {
		names[record.Service] = true
	}
	return distinctRows(names, "service", 500)
}

func attributeKeys(records []Record, filter query.Filter) []map[string]any {
	keys := make(map[string]bool)
	for _, record := range records {
		attributes := record.Attributes
		if filter.DiscoveryScope == "resource" {
			attributes = record.ResourceAttributes
		}
		for key := range attributes {
			if contains(key, filter.KeySearch) {
				keys[key] = true
			}
		}
	}
	return distinctRows(keys, "key", 51)
}

func distinctRows(values map[string]bool, field string, limit int) []map[string]any {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, min(len(names), limit))
	for _, name := range names[:min(len(names), limit)] {
		rows = append(rows, map[string]any{field: name})
	}
	return rows
}
