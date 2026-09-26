package model

import (
	"fmt"
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
	Outage          bool `json:"outage"`
	ConfidenceLevel bool `json:"confidence_level"`
}

type Feed struct {
	start      time.Time
	epoch      string
	interval   time.Duration
	outage     atomic.Bool
	confidence atomic.Bool
}

func NewFeed(start time.Time, epoch string, interval time.Duration) *Feed {
	return &Feed{start: start, epoch: epoch, interval: interval}
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
	return State{Outage: f.outage.Load(), ConfidenceLevel: f.confidence.Load()}
}

func (f *Feed) VolcanicReports(since, now time.Time) []VolcanicReport {
	reports := []VolcanicReport{}
	count := 20 + int(now.Sub(f.start)/f.interval)
	confidence := f.confidence.Load()
	for i := 0; i < count; i++ {
		reported := f.start.Add(time.Duration(i-19) * f.interval)
		if reported.Before(since) {
			continue
		}

		report := VolcanicReport{
			ReportID:         fmt.Sprintf("pvmbg-%s-%d", f.epoch, i),
			VolcanoID:        []string{"MERAPI", "SEMERU", "ANAK_KRAKATAU"}[i%3],
			AlertLevel:       []string{"Normal", "Waspada", "Siaga", "Awas"}[i%4],
			EruptionCount24h: i % 12,
			AshColumnHeightM: float64((i % 8) * 100),
			ReportedAt:       reported,
		}
		if confidence {
			value := 0.85
			report.ConfidenceLevel = &value
		}

		reports = append(reports, report)
	}

	return reports
}
