package dbtrace

import (
	"go.opentelemetry.io/otel/attribute"
	"strings"
	"testing"
)

func attributesMap(attrs []attribute.KeyValue) map[string]any {
	result := map[string]any{}
	for _, attr := range attrs {
		result[string(attr.Key)] = attr.Value.AsInterface()
	}
	return result
}

func TestQueryDetails(t *testing.T) {
	tests := []struct {
		name, query, summary string
		hidden               []string
	}{
		{"parameterized", `SELECT * FROM "hazard_events" WHERE source = $1 AND id > $2 ORDER BY id LIMIT 100`, "SELECT hazard_events", []string{"100"}},
		{"insert", `INSERT INTO "hazard_events" (source, source_ref_id) VALUES ($1, $2) RETURNING id`, "INSERT hazard_events", nil},
		{"literal and comment", `SELECT * FROM source_poll_states WHERE source = 'private-value' /* secret-comment */ AND failures = 987654321 -- secret-line
`, "SELECT source_poll_states", []string{"private-value", "secret-comment", "secret-line", "987654321"}},
		{"dollar quoted", `SELECT $$dollar-secret$$, $body$another-secret$body$, E'escaped-secret' FROM hazard_events`, "SELECT hazard_events", []string{"dollar-secret", "another-secret", "escaped-secret"}},
		{"json", `UPDATE hazard_events SET attributes = '{"secret":"payload-value"}'::jsonb WHERE id = $1`, "UPDATE hazard_events", []string{"payload-value"}},
		{"advisory lock", `SELECT pg_advisory_xact_lock(hashtextextended('private-source-key', 0))`, "SELECT", []string{"private-source-key"}},
		{"transaction", `BEGIN`, "BEGIN", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			summary, attrs := queryDetails(tc.query)
			values := attributesMap(attrs)
			if summary != tc.summary {
				t.Fatalf("summary %q, want %q; attrs=%v", summary, tc.summary, values)
			}
			text, ok := values["db.query.text"].(string)
			if !ok || text == "" {
				t.Fatal("missing query text", values)
			}
			for _, hidden := range tc.hidden {
				if strings.Contains(text, hidden) || strings.Contains(summary, hidden) {
					t.Fatalf("sensitive value %q in %v", hidden, values)
				}
			}
			if tc.name == "parameterized" && (!strings.Contains(text, "$1") || !strings.Contains(text, "$2")) {
				t.Fatalf("placeholders lost: %s", text)
			}
		})
	}
}

func TestQueryDetailsBoundsAndInvalidInput(t *testing.T) {
	summary, attrs := queryDetails(strings.Repeat("private-overlarge ", maxQueryInput))
	if summary != "postgres query" || attributesMap(attrs)["db.query.text"] != nil {
		t.Fatal("oversized query must not be exported")
	}
	for _, query := range []string{"", `SELECT 'unfinished-secret`, `SELECT $unterminated$private-secret`, `/* outer /* inner */ nested-secret */ SELECT 1`} {
		_, attrs := queryDetails(query)
		for _, attr := range attrs {
			if strings.Contains(attr.Value.AsString(), "secret") {
				t.Fatalf("unclosed literal exposed: %+v", attrs)
			}
		}
	}
}
