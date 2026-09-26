package model

import (
	"testing"
	"time"
)

// A new feed begins with 20 historical events.
func TestSeismicSeedCount(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)
	if events := feed.SeismicEvents(time.Time{}, start); len(events) != 20 {
		t.Fatalf("seed count: %d", len(events))
	}
}

// Two elapsed intervals add exactly two events.
func TestSeismicTimelineGrowth(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)

	now := start.Add(2 * time.Second)
	events := feed.SeismicEvents(time.Time{}, now)
	if len(events) != 22 {
		t.Fatalf("timeline count: %d", len(events))
	}
}

// Warnings reference tsunami-capable events and arrive 30 minutes later.
func TestWarningCorrelation(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)
	now := start.Add(2 * time.Second)
	events := feed.SeismicEvents(time.Time{}, now)

	byID := map[string]SeismicEvent{}
	for _, event := range events {
		byID[event.EventID] = event
	}
	warnings := feed.TsunamiWarnings(time.Time{}, now)
	if len(warnings) != 6 {
		t.Fatalf("warning count: %d", len(warnings))
	}
	for _, warning := range warnings {
		event, ok := byID[warning.RelatedEventID]
		if !ok || !event.PotentialTsunami || !warning.EstimatedArrival.Equal(event.OccurredAt.Add(30*time.Minute)) {
			t.Fatalf("invalid warning correlation: %+v", warning)
		}
	}
}

// Both endpoints include the event exactly at since; warnings use event time.
func TestSinceIncludesBoundaryEvent(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)
	now := start.Add(2 * time.Second)

	since := start.Add(time.Second)
	filtered := feed.SeismicEvents(since, now)
	if len(filtered) != 2 || !filtered[0].OccurredAt.Equal(since) {
		t.Fatalf("since must be inclusive: %+v", filtered)
	}
	if warnings := feed.TsunamiWarnings(since, now); len(warnings) != 1 || warnings[0].RelatedEventID != filtered[0].EventID {
		t.Fatalf("warning cursor must follow event time: %+v", warnings)
	}
}

// An empty result must serialize as [] rather than null.
func TestEmptyEventsReturnArray(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)
	now := start.Add(2 * time.Second)

	if events := feed.SeismicEvents(now.Add(time.Second), now); events == nil || len(events) != 0 {
		t.Fatal("empty response must encode as an array")
	}
}
