package model

import (
	"slices"
	"testing"
	"time"
)

// A new feed begins with 20 historical events.
func TestSeismicSeedCount(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	if events := feed.SeismicEvents(time.Time{}, start); len(events) != 20 {
		t.Fatalf("seed count: %d", len(events))
	}
}

// Two elapsed intervals add exactly two events.
func TestSeismicTimelineGrowth(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)

	now := start.Add(2 * time.Second)
	events := feed.SeismicEvents(time.Time{}, now)
	if len(events) != 22 {
		t.Fatalf("timeline count: %d", len(events))
	}
}

// Warnings reference tsunami-capable events and arrive 30 minutes later.
func TestWarningCorrelation(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	now := start.Add(2 * time.Second)
	events := feed.SeismicEvents(time.Time{}, now)

	byID := map[string]SeismicEvent{}
	for _, event := range events {
		byID[event.EventID] = event
	}
	warnings := feed.TsunamiWarnings(time.Time{}, now)
	if len(warnings) == 0 {
		t.Fatal("expected tsunami warnings")
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
	feed := NewFeed(start, time.Second)
	now := start.Add(2 * time.Second)

	since := start
	filtered := feed.SeismicEvents(since, now)
	if len(filtered) != 3 || !filtered[0].OccurredAt.Equal(since) {
		t.Fatalf("since must be inclusive: %+v", filtered)
	}
	if warnings := feed.TsunamiWarnings(since, now); len(warnings) != 1 || warnings[0].RelatedEventID != filtered[0].EventID {
		t.Fatalf("warning cursor must follow event time: %+v", warnings)
	}
}

// An empty result must serialize as [] rather than null.
func TestEmptyEventsReturnArray(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	now := start.Add(2 * time.Second)

	if events := feed.SeismicEvents(now.Add(time.Second), now); events == nil || len(events) != 0 {
		t.Fatal("empty response must encode as an array")
	}
}

// Restarting at a later time preserves every overlapping time bucket.
func TestSeismicTimelineIsDeterministicAcrossRestarts(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	restarted := start.Add(2 * time.Second)
	now := restarted.Add(2 * time.Second)

	before := NewFeed(start, time.Second).SeismicEvents(restarted, now)
	after := NewFeed(restarted, time.Second).SeismicEvents(restarted, now)
	if len(before) != len(after) {
		t.Fatalf("overlap length changed after restart: %d != %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("bucket changed after restart: %+v != %+v", before[i], after[i])
		}
	}

	warningsBefore := NewFeed(start, time.Second).TsunamiWarnings(restarted, now)
	warningsAfter := NewFeed(restarted, time.Second).TsunamiWarnings(restarted, now)
	if len(warningsBefore) != len(warningsAfter) {
		t.Fatalf("warning overlap changed after restart: %d != %d", len(warningsBefore), len(warningsAfter))
	}
	for i := range warningsBefore {
		if warningsBefore[i].WarningID != warningsAfter[i].WarningID ||
			warningsBefore[i].RelatedEventID != warningsAfter[i].RelatedEventID ||
			warningsBefore[i].ThreatLevel != warningsAfter[i].ThreatLevel ||
			!slices.Equal(warningsBefore[i].AffectedZones, warningsAfter[i].AffectedZones) ||
			!warningsBefore[i].EstimatedArrival.Equal(warningsAfter[i].EstimatedArrival) {
			t.Fatalf("warning changed after restart: %+v != %+v", warningsBefore[i], warningsAfter[i])
		}
	}
}
