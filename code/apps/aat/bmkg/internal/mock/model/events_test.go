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
	events := feed.SeismicEvents(time.Time{}, start)
	if len(events) != 20 {
		t.Fatalf("seed count: %d", len(events))
	}
	if !feed.start.Equal(start.Add(-19*time.Second)) || !events[0].OccurredAt.Equal(feed.start) || !events[19].OccurredAt.Equal(start) {
		t.Fatal("seed timeline must span startup minus 19 intervals through startup")
	}
}

// Ten-second intervals align startup and cursors to wall-clock boundaries.
func TestTenSecondAlignment(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 27, 123, time.UTC)
	feed := NewFeed(start, 10*time.Second)
	seed := feed.SeismicEvents(time.Time{}, start)
	if len(seed) != 20 || !seed[19].OccurredAt.Equal(start.Truncate(10*time.Second)) {
		t.Fatalf("unexpected aligned seed: %+v", seed)
	}
	since := start
	now := time.Date(2026, 9, 26, 0, 1, 5, 0, time.UTC)
	events := feed.SeismicEvents(since, now)
	if len(events) != 4 || events[0].OccurredAt.Second() != 30 || events[3].OccurredAt.Second() != 0 {
		t.Fatalf("expected 00:30, 00:40, 00:50, 01:00: %+v", events)
	}
	for _, event := range append(seed, events...) {
		if event.OccurredAt.Second()%10 != 0 || event.OccurredAt.Nanosecond() != 0 {
			t.Fatalf("unaligned event: %+v", event)
		}
	}
	for _, warning := range feed.TsunamiWarnings(since, now) {
		occurred := warning.EstimatedArrival.Add(-30 * time.Minute)
		if occurred.Before(since) || occurred.After(now) || occurred.Second()%10 != 0 || occurred.Nanosecond() != 0 {
			t.Fatalf("warning outside aligned range: %+v", warning)
		}
	}
}

// Requested bounds between intervals must not leak earlier or later events.
func TestSeismicEventsRespectTimeRange(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	since := start.Add(500 * time.Millisecond)
	now := start.Add(2500 * time.Millisecond)
	events := feed.SeismicEvents(since, now)
	if len(events) != 2 || !events[0].OccurredAt.Equal(start.Add(time.Second)) || !events[1].OccurredAt.Equal(start.Add(2*time.Second)) {
		t.Fatalf("events must stay within since and now: %+v", events)
	}
	// The startup bucket has a warning; a later cursor must exclude it.
	if warnings := feed.TsunamiWarnings(since, now); len(warnings) != 0 {
		t.Fatalf("warning before since returned: %+v", warnings)
	}
	if events := feed.SeismicEvents(time.Time{}, feed.start.Add(-time.Nanosecond)); len(events) != 0 {
		t.Fatalf("events after now returned: %+v", events)
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
