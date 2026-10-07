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
	interval time.Duration
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

// SeismicEvents and TsunamiWarnings are deterministic by using the timestamp to generate the data
// The same timestamp will always produce the same event data.

func (f *Feed) SeismicEvents(since, now time.Time) []SeismicEvent {
	events := []SeismicEvent{}
	occurred := f.firstOccurrence(since)
	for !occurred.After(now) {
		slot := occurred.UnixNano() / f.interval.Nanoseconds()

		events = append(events, SeismicEvent{
			EventID:          fmt.Sprintf("bmkg-%d", occurred.UnixNano()),
			Magnitude:        []float64{7.1, 4.2, 5.0, 6.5}[slot%4],
			DepthKM:          float64(10 + slot%30),
			EpicenterLat:     -8,
			EpicenterLon:     110,
			RegionName:       "Pesisir Selatan Jawa",
			OccurredAt:       occurred,
			PotentialTsunami: slot%4 == 0,
		})
		occurred = occurred.Add(f.interval)
	}

	return events
}

func (f *Feed) TsunamiWarnings(since, now time.Time) []TsunamiWarning {
	warnings := []TsunamiWarning{}
	occurred := f.firstOccurrence(since)
	for !occurred.After(now) {
		slot := occurred.UnixNano() / f.interval.Nanoseconds()
		if slot%4 == 0 {
			id := fmt.Sprintf("bmkg-%d", occurred.UnixNano())
			warnings = append(warnings, TsunamiWarning{
				WarningID:        "warning-" + id,
				RelatedEventID:   id,
				ThreatLevel:      []string{"Waspada", "Siaga", "Awas"}[(slot/4)%3],
				AffectedZones:    []string{"Pesisir Selatan Jawa"},
				EstimatedArrival: occurred.Add(30 * time.Minute),
			})
		}
		occurred = occurred.Add(f.interval)
	}

	return warnings
}
