package controller

import (
	"encoding/json"

	"aat/aggregator/internal/model"
)

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
func warningFixture(level string) model.Record {
	return record(`{"warning_id":"tw-1","related_event_id":"eq-1","threat_level":"` + level + `","affected_zones":["Jawa"],"estimated_arrival":"2026-09-25T00:30:00Z"}`)
}
