package aggregate

import (
	"encoding/json"
	"testing"
)

// record builds a raw source fixture; malformed fixture JSON is a test setup error.
func record(s string) Record {
	var r Record
	if e := json.Unmarshal([]byte(s), &r); e != nil {
		panic(e)
	}

	return r
}

// quake returns a valid seismic event with a configurable magnitude.
func quake(magnitude string) Record {
	return record(`{"event_id":"eq-1","magnitude":` + magnitude + `,"depth_km":10,"epicenter_lat":-8,"epicenter_lon":110,"region_name":"Jawa","occurred_at":"2026-09-25T00:00:00Z","potential_tsunami":true}`)
}

// report returns a valid Merapi report with a configurable source ID.
func report(id string) Record {
	return record(`{"report_id":"` + id + `","volcano_id":"MERAPI","alert_level":"Siaga","eruption_count_24h":2,"ash_column_height_m":300,"reported_at":"2026-09-25T00:00:00Z"}`)
}

// tsunami references the quake fixture at the requested threat level.
func tsunami(level string) Record {
	return record(`{"warning_id":"tw-1","related_event_id":"eq-1","threat_level":"` + level + `","affected_zones":["Jawa"],"estimated_arrival":"2026-09-25T00:30:00Z"}`)
}

// Magnitude boundaries select the canonical seismic severity.
func TestSeismicSeverityByMagnitude(t *testing.T) {
	for _, tc := range []struct{ m, want string }{
		{"4.99", "NORMAL"},
		{"5", "WASPADA"},
		{"6.49", "WASPADA"},
		{"6.5", "SIAGA"},
	} {
		h, e := Seismic(quake(tc.m), nil)
		if e != nil || h.Severity != tc.want {
			t.Fatalf("%s: %+v %v", tc.m, h, e)
		}
	}
}

// A related warning overrides magnitude-based severity.
func TestWarningOverridesSeismicSeverity(t *testing.T) {
	for _, level := range []string{"Waspada", "Siaga", "Awas"} {
		h, e := Seismic(quake("7"), []Record{tsunami(level)})
		if e != nil || h.Severity != levels[level] {
			t.Fatalf("warning override: %+v %v", h, e)
		}
	}
}

// Mapping retains additive fields, resolves Merapi coordinates, and keeps its stable ID.
func TestVolcanicAttributesAndReference(t *testing.T) {
	r := report("v1")
	r["confidence_level"] = json.RawMessage(`0.85`)
	r["future_sensor"] = json.RawMessage(`{"nested":[1,true]}`)
	h, e := Volcanic(r)
	if e != nil {
		t.Fatal(e)
	}

	if h.Area != "Gunung Merapi" || h.Latitude != -7.54 || string(h.Attributes["confidence_level"]) != "0.85" || string(h.Attributes["future_sensor"]) != `{"nested":[1,true]}` {
		t.Fatalf("bad mapping: %+v", h)
	}

	old, _ := Volcanic(report("v1"))
	if old.ID != h.ID {
		t.Fatal("unstable id")
	}
}

// Unknown reference data and out-of-range confidence are rejected independently.
func TestVolcanicRejectsInvalidFields(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       json.RawMessage
	}{
		{"unknown_volcano", "volcano_id", json.RawMessage(`"UNKNOWN"`)},
		{"confidence_above_one", "confidence_level", json.RawMessage(`1.1`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := report("v1")
			input[tc.field] = tc.value
			if _, err := Volcanic(input); err == nil {
				t.Fatalf("invalid %s accepted", tc.field)
			}
		})
	}
}

// Each case starts valid, then introduces exactly one breaking schema change.
func TestSeismicRejectsBreakingChanges(t *testing.T) {
	t.Run("magnitude_has_wrong_type", func(t *testing.T) {
		input := quake("5")
		input["magnitude"] = json.RawMessage(`"five"`)
		if _, err := Seismic(input, nil); err == nil {
			t.Fatal("string magnitude accepted")
		}
	})

	t.Run("event_id_is_missing", func(t *testing.T) {
		input := quake("5")
		delete(input, "event_id")
		if _, err := Seismic(input, nil); err == nil {
			t.Fatal("missing event ID accepted")
		}
	})
}
