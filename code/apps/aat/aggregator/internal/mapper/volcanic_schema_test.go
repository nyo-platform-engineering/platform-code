package mapper

import (
	"encoding/json"
	"testing"

	"aat/aggregator/internal/model"
)

func TestVolcanicV2RequiresExplicitCompatibility(t *testing.T) {
	const original = `{"report_id":"v1","volcano_id":"MERAPI","alert_level":"Siaga","eruption_count_24h":2,"ash_column_height_m":100,"reported_at":"2026-11-18T00:00:00Z"}`
	for _, mode := range []string{"rename", "type", "missing"} {
		var record model.Record
		if err := json.Unmarshal([]byte(original), &record); err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "rename":
			record["level"] = record["alert_level"]
			delete(record, "alert_level")
		case "type":
			record["eruption_count_24h"] = json.RawMessage(`"2"`)
		case "missing":
			delete(record, "eruption_count_24h")
		}
		if _, err := Volcanic(record); err == nil {
			t.Fatalf("strict mapper accepted %s", mode)
		}
		event, err := VolcanicV2(record)
		if err != nil || event.Severity != "SIAGA" {
			t.Fatalf("v2 rejected %s: %+v %v", mode, event, err)
		}
		if mode == "missing" {
			if _, exists := event.Attributes["eruption_count_24h"]; exists {
				t.Fatal("missing count was fabricated")
			}
		}
		if mode == "rename" {
			if _, mutated := record["alert_level"]; mutated {
				t.Fatal("normalizer modified raw record")
			}
			record["alert_level"] = record["level"]
			if _, err := VolcanicV2(record); err == nil {
				t.Fatal("ambiguous alias accepted")
			}
		}
	}
}
