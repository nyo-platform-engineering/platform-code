package model

import (
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

type VolcanicReport struct {
	ReportID         string    `json:"report_id"`
	VolcanoID        string    `json:"volcano_id"`
	AlertLevel       string    `json:"alert_level"`
	EruptionCount24h int       `json:"eruption_count_24h"`
	AshColumnHeightM float64   `json:"ash_column_height_m"`
	ReportedAt       time.Time `json:"reported_at"`
	ConfidenceLevel  *float64  `json:"confidence_level,omitempty"`
}

type State struct {
	Outage          bool   `json:"outage"`
	ConfidenceLevel bool   `json:"confidence_level"`
	SchemaMode      string `json:"schema_mode"`
}

type Feed struct {
	start      time.Time
	interval   time.Duration
	outage     atomic.Bool
	confidence atomic.Bool
	schemaMode atomic.Uint32
}

func NewFeed(start time.Time, interval time.Duration) *Feed {
	// seed initial 20 by shifting the start time
	// Truncate() run down from start to the nearest interval,
	// e.g. interval = 10s 10:00:05 to 10:00:00
	// -19 * interval = 190s or 3m10s
	// makes it 9:56:50
	// with this we generate 20 events from 9:56:50 to 10:00:00
	return &Feed{
		start:    start.UTC().Truncate(interval).Add(-19 * interval),
		interval: interval,
	}
}

func (f *Feed) firstOccurrence(since time.Time) time.Time {
	if !since.After(f.start) {
		return f.start
	}
	// since truncate runs down, need to make sure first after since,
	// so add interval if truncated time is before since
	first := since.UTC().Truncate(f.interval)
	if first.Before(since) {
		first = first.Add(f.interval)
	}
	return first
}

func (f *Feed) IsOutage() bool {
	return f.outage.Load()
}

func (f *Feed) SetOutage(enabled bool) {
	f.outage.Store(enabled)
}

func (f *Feed) SetConfidence(enabled bool) {
	f.confidence.Store(enabled)
}

func (f *Feed) State() State {
	return State{Outage: f.outage.Load(), ConfidenceLevel: f.confidence.Load(), SchemaMode: f.SchemaMode()}
}

func (f *Feed) SchemaMode() string {
	return []string{"original", "rename_field", "type_change", "missing_required"}[f.schemaMode.Load()]
}

func (f *Feed) SetSchemaMode(mode string) bool {
	for index, name := range []string{"original", "rename_field", "type_change", "missing_required"} {
		if mode == name {
			f.schemaMode.Store(uint32(index))
			f.confidence.Store(false)
			return true
		}
	}
	if mode == "additive" {
		f.schemaMode.Store(0)
		f.confidence.Store(true)
		return true
	}
	return false
}

func (f *Feed) PayloadReports(since, now time.Time) []map[string]any {
	mode := f.SchemaMode()
	rows := []map[string]any{}
	for _, report := range f.VolcanicReports(since, now) {
		row := map[string]any{"report_id": report.ReportID, "volcano_id": report.VolcanoID, "alert_level": report.AlertLevel,
			"eruption_count_24h": report.EruptionCount24h, "ash_column_height_m": report.AshColumnHeightM, "reported_at": report.ReportedAt}
		if report.ConfidenceLevel != nil {
			row["confidence_level"] = *report.ConfidenceLevel
		}
		switch mode {
		case "rename_field":
			row["level"] = row["alert_level"]
			delete(row, "alert_level")
		case "type_change":
			row["eruption_count_24h"] = strconv.Itoa(report.EruptionCount24h)
		case "missing_required":
			delete(row, "eruption_count_24h")
		}
		rows = append(rows, row)
	}
	return rows
}

// VolcanicReports is deterministic by using the timestamp to generate the data
// The same timestamp will always produce the same event data.

func (f *Feed) VolcanicReports(since, now time.Time) []VolcanicReport {
	reports := []VolcanicReport{}
	confidence := f.confidence.Load()
	reported := f.firstOccurrence(since)
	for !reported.After(now) {
		slot := reported.UnixNano() / f.interval.Nanoseconds()

		report := VolcanicReport{
			ReportID:         fmt.Sprintf("pvmbg-%d", reported.UnixNano()),
			VolcanoID:        []string{"MERAPI", "SEMERU", "ANAK_KRAKATAU"}[slot%3],
			AlertLevel:       []string{"Normal", "Waspada", "Siaga", "Awas"}[slot%4],
			EruptionCount24h: int(slot % 12),
			AshColumnHeightM: float64((slot % 8) * 100),
			ReportedAt:       reported,
		}
		if confidence {
			value := 0.85
			report.ConfidenceLevel = &value
		}

		reports = append(reports, report)
		reported = reported.Add(f.interval)
	}

	return reports
}
