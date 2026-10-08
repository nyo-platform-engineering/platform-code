package mapper_test

import (
	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"
	"encoding/json"
	"time"

	"testing"
)

func TestHazardIDsSortByOccurrenceAndRemainStable(t *testing.T) {
	at := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	first := mapper.Base("BMKG", "first", "SEISMIC", at)
	next := mapper.Base("PVMBG", "second", "VOLCANIC", at.Add(time.Nanosecond))
	later := mapper.Base("BMKG", "third", "SEISMIC", at.Add(time.Second))
	if !(first.ID < next.ID && next.ID < later.ID) {
		t.Fatalf("IDs are not in occurrence order: %s, %s, %s", first.ID, next.ID, later.ID)
	}
	repeated := mapper.Base("BMKG", "first", "SEISMIC", at.In(time.FixedZone("WIB", 7*60*60)))
	if first.ID != repeated.ID {
		t.Fatal("the same source record changed ID on remapping or timezone conversion")
	}
	otherSource := mapper.Base("PVMBG", "first", "VOLCANIC", at)
	if first.ID == otherSource.ID {
		t.Fatal("different sources share a hazard ID")
	}
}

// record builds a raw source fixture; malformed fixture JSON is a test setup error.
func record(s string) model.Record {
	var r model.Record
	if e := json.Unmarshal([]byte(s), &r); e != nil {
		panic(e)
	}

	return r
}

// quake returns a valid seismic event with a configurable magnitude.
func quake(magnitude string) model.Record {
	return record(`{"event_id":"eq-1","magnitude":` + magnitude + `,"depth_km":10,"epicenter_lat":-8,"epicenter_lon":110,"region_name":"Jawa","occurred_at":"2026-09-25T00:00:00Z","potential_tsunami":true}`)
}

// report returns a valid Merapi report with a configurable source ID.
func report(id string) model.Record {
	return record(`{"report_id":"` + id + `","volcano_id":"MERAPI","alert_level":"Siaga","eruption_count_24h":2,"ash_column_height_m":300,"reported_at":"2026-09-25T00:00:00Z"}`)
}

// tsunami references the quake fixture at the requested threat level.
func tsunami(level string) model.Record {
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
		h, e := mapper.Seismic(quake(tc.m), nil)
		if e != nil || h.Severity != tc.want {
			t.Fatalf("%s: %+v %v", tc.m, h, e)
		}
	}
}

// A related warning overrides magnitude-based severity.
func TestWarningOverridesSeismicSeverity(t *testing.T) {
	for _, level := range []string{"Waspada", "Siaga", "Awas"} {
		h, e := mapper.Seismic(quake("7"), []model.Record{tsunami(level)})
		if e != nil || h.Severity != mapper.Levels[level] {
			t.Fatalf("warning override: %+v %v", h, e)
		}
	}
}

// Enrichment uses the highest warning level and preserves unmapped source attributes.
func TestSeismicWarningEnrichmentPreservesRawAttributes(t *testing.T) {
	input := quake("7")
	input["future_sensor"] = json.RawMessage(`{"nested":[1,true]}`)
	before, _ := json.Marshal(input)
	h, err := mapper.Seismic(input, []model.Record{tsunami("Awas"), tsunami("Waspada")})
	if err != nil {
		t.Fatal(err)
	}
	if h.Severity != "AWAS" || string(h.Attributes["magnitude"]) != "7" || string(h.Attributes["future_sensor"]) != `{"nested":[1,true]}` || len(h.Attributes) != 5 {
		t.Fatalf("incorrect enrichment: %+v", h)
	}
	var warnings []model.Record
	if err := json.Unmarshal(h.Attributes["tsunami_warnings"], &warnings); err != nil || len(warnings) != 2 {
		t.Fatalf("warnings not preserved: %v, %v", warnings, err)
	}
	if _, ok := h.Attributes["event_id"]; ok {
		t.Fatal("canonical reference duplicated in attributes")
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("source record modified")
	}
}

// Invalid correlations must fail rather than produce an enriched hazard.
func TestSeismicRejectsInvalidWarningEnrichment(t *testing.T) {
	for _, name := range []string{"no_tsunami_potential", "unrelated_warning", "reserved_attribute", "invalid_warning_level"} {
		t.Run(name, func(t *testing.T) {
			input := quake("7")
			warning := tsunami("Awas")
			switch name {
			case "no_tsunami_potential":
				input["potential_tsunami"] = json.RawMessage(`false`)
			case "unrelated_warning":
				warning["related_event_id"] = json.RawMessage(`"another-event"`)
			case "reserved_attribute":
				input["tsunami_warnings"] = json.RawMessage(`[]`)
			case "invalid_warning_level":
				warning["threat_level"] = json.RawMessage(`"Unknown"`)
			}
			if _, err := mapper.Seismic(input, []model.Record{warning}); err == nil {
				t.Fatal("invalid enrichment accepted")
			}
		})
	}
}

// Mapping preserves unmapped fields, resolves Merapi coordinates, and keeps its stable ID.
func TestVolcanicAttributesAndReference(t *testing.T) {
	r := report("v1")
	r["confidence_level"] = json.RawMessage(`0.85`)
	r["future_sensor"] = json.RawMessage(`{"nested":[1,true]}`)
	h, e := mapper.Volcanic(r)
	if e != nil {
		t.Fatal(e)
	}

	if h.Area != "Gunung Merapi" || h.Latitude != -7.54 || string(h.Attributes["confidence_level"]) != "0.85" || string(h.Attributes["future_sensor"]) != `{"nested":[1,true]}` || len(h.Attributes) != 5 {
		t.Fatalf("bad mapping: %+v", h)
	}

	old, _ := mapper.Volcanic(report("v1"))
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
			if _, err := mapper.Volcanic(input); err == nil {
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
		if _, err := mapper.Seismic(input, nil); err == nil {
			t.Fatal("string magnitude accepted")
		}
	})

	t.Run("event_id_is_missing", func(t *testing.T) {
		input := quake("5")
		delete(input, "event_id")
		if _, err := mapper.Seismic(input, nil); err == nil {
			t.Fatal("missing event ID accepted")
		}
	})
}

// Validation rejects missing/null fields before decoding can default them to zero.
func TestSourceFieldValidation(t *testing.T) {
	for _, tc := range []struct {
		name, source, field string
		value               json.RawMessage
	}{
		{"missing_potential", "BMKG", "potential_tsunami", nil},
		{"null_potential", "BMKG", "potential_tsunami", json.RawMessage(`null`)},
		{"empty_region", "BMKG", "region_name", json.RawMessage(`""`)},
		{"latitude_out_of_range", "BMKG", "epicenter_lat", json.RawMessage(`91`)},
		{"negative_depth", "BMKG", "depth_km", json.RawMessage(`-1`)},
		{"missing_eruption_count", "PVMBG", "eruption_count_24h", nil},
		{"fractional_eruption_count", "PVMBG", "eruption_count_24h", json.RawMessage(`1.5`)},
		{"negative_ash_height", "PVMBG", "ash_column_height_m", json.RawMessage(`-1`)},
		{"null_confidence", "PVMBG", "confidence_level", json.RawMessage(`null`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := quake("5")
			if tc.source == "PVMBG" {
				input = report("v1")
			}
			if tc.value == nil {
				delete(input, tc.field)
			} else {
				input[tc.field] = tc.value
			}
			var err error
			if tc.source == "BMKG" {
				_, err = mapper.Seismic(input, nil)
			} else {
				_, err = mapper.Volcanic(input)
			}
			if err == nil {
				t.Fatalf("invalid %s accepted", tc.field)
			}
		})
	}
}
