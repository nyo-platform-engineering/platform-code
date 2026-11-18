package mock

import (
	"testing"
	"time"
)

func TestBreakingSchemaModesAndLegacyReset(t *testing.T) {
	handler := NewHandler("pvmbg-test-only", time.Now().UTC(), time.Hour, 0, 0)
	for _, mode := range []string{"rename_field", "type_change", "missing_required"} {
		result := requestPVMBG(handler, "POST", "/admin/schema-version", "pvmbg-test-only", `{"mode":"`+mode+`"}`)
		requireStatus(t, result, 200)
		row := fetchReports(t, handler)[0]
		switch mode {
		case "rename_field":
			if _, old := row["alert_level"]; old || row["level"] == nil {
				t.Fatal("field was not renamed")
			}
		case "type_change":
			if _, ok := row["eruption_count_24h"].(string); !ok {
				t.Fatal("count did not become text")
			}
		case "missing_required":
			if _, exists := row["eruption_count_24h"]; exists {
				t.Fatal("required field is still present")
			}
		}
	}
	requireStatus(t, requestPVMBG(handler, "POST", "/admin/schema-version", "pvmbg-test-only", `{"enabled":false}`), 200)
	row := fetchReports(t, handler)[0]
	if _, ok := row["eruption_count_24h"].(float64); !ok || row["alert_level"] == nil {
		t.Fatal("legacy reset did not restore original schema")
	}
	for _, body := range []string{`{"mode":"unknown"}`, `{"mode":"original","enabled":true}`, `{}`} {
		requireStatus(t, requestPVMBG(handler, "POST", "/admin/schema-version", "pvmbg-test-only", body), 400)
	}
}
