package model

import (
	"fmt"
	"time"
)

type SeismicEvent struct {
	EventID          string    `json:"event_id"`
	Magnitude        float64   `json:"magnitude"`
	DepthKM          float64   `json:"depth_km"`
	EpicenterLat     float64   `json:"epicenter_lat"`
	EpicenterLon     float64   `json:"epicenter_lon"`
	RegionName       string    `json:"region_name"`
	OccurredAt       time.Time `json:"occurred_at"`
	PotentialTsunami bool      `json:"potential_tsunami"`
}

type TsunamiWarning struct {
	WarningID        string    `json:"warning_id"`
	RelatedEventID   string    `json:"related_event_id"`
	ThreatLevel      string    `json:"threat_level"`
	AffectedZones    []string  `json:"affected_zones"`
	EstimatedArrival time.Time `json:"estimated_arrival"`
}

type Feed struct {
	start    time.Time
	epoch    string
	interval time.Duration
}

func NewFeed(start time.Time, epoch string, interval time.Duration) *Feed {
	return &Feed{start: start, epoch: epoch, interval: interval}
}

func (f *Feed) SeismicEvents(since, now time.Time) []SeismicEvent {
	events := []SeismicEvent{}
	count := 20 + int(now.Sub(f.start)/f.interval)
	for i := 0; i < count; i++ {
		occurred := f.start.Add(time.Duration(i-19) * f.interval)
		if occurred.Before(since) {
			continue
		}

		events = append(events, SeismicEvent{
			EventID:          fmt.Sprintf("bmkg-%s-%d", f.epoch, i),
			Magnitude:        []float64{7.1, 4.2, 5.0, 6.5}[i%4],
			DepthKM:          float64(10 + i%30),
			EpicenterLat:     -8,
			EpicenterLon:     110,
			RegionName:       "Pesisir Selatan Jawa",
			OccurredAt:       occurred,
			PotentialTsunami: i%4 == 0,
		})
	}

	return events
}

func (f *Feed) TsunamiWarnings(since, now time.Time) []TsunamiWarning {
	warnings := []TsunamiWarning{}
	count := 20 + int(now.Sub(f.start)/f.interval)
	for i := 0; i < count; i++ {
		occurred := f.start.Add(time.Duration(i-19) * f.interval)
		if occurred.Before(since) || i%4 != 0 {
			continue
		}

		id := fmt.Sprintf("bmkg-%s-%d", f.epoch, i)
		warnings = append(warnings, TsunamiWarning{
			WarningID:        "warning-" + id,
			RelatedEventID:   id,
			ThreatLevel:      []string{"Waspada", "Siaga", "Awas"}[(i/4)%3],
			AffectedZones:    []string{"Pesisir Selatan Jawa"},
			EstimatedArrival: occurred.Add(30 * time.Minute),
		})
	}

	return warnings
}
