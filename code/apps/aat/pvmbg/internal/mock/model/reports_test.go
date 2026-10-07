package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A new feed begins with 20 historical reports.
func TestVolcanicSeedCount(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	reports := feed.VolcanicReports(time.Time{}, start)
	if len(reports) != 20 {
		t.Fatalf("seed count: %d", len(reports))
	}
	if !feed.start.Equal(start.Add(-19*time.Second)) || !reports[0].ReportedAt.Equal(feed.start) || !reports[19].ReportedAt.Equal(start) {
		t.Fatal("seed timeline must span startup minus 19 intervals through startup")
	}
}

// Ten-second intervals align startup and cursors to wall-clock boundaries.
func TestTenSecondAlignment(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 27, 123, time.UTC)
	feed := NewFeed(start, 10*time.Second)
	seed := feed.VolcanicReports(time.Time{}, start)
	if len(seed) != 20 || !seed[19].ReportedAt.Equal(start.Truncate(10*time.Second)) {
		t.Fatalf("unexpected aligned seed: %+v", seed)
	}
	since := start
	now := time.Date(2026, 9, 26, 0, 1, 5, 0, time.UTC)
	reports := feed.VolcanicReports(since, now)
	if len(reports) != 4 || reports[0].ReportedAt.Second() != 30 || reports[3].ReportedAt.Second() != 0 {
		t.Fatalf("expected 00:30, 00:40, 00:50, 01:00: %+v", reports)
	}
	for _, report := range append(seed, reports...) {
		if report.ReportedAt.Second()%10 != 0 || report.ReportedAt.Nanosecond() != 0 {
			t.Fatalf("unaligned report: %+v", report)
		}
	}
}

// Requested bounds between intervals must not leak earlier or later events.
func TestVolcanicReportsRespectTimeRange(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	since := start.Add(500 * time.Millisecond)
	now := start.Add(2500 * time.Millisecond)
	reports := feed.VolcanicReports(since, now)
	if len(reports) != 2 || !reports[0].ReportedAt.Equal(start.Add(time.Second)) || !reports[1].ReportedAt.Equal(start.Add(2*time.Second)) {
		t.Fatalf("reports must stay within since and now: %+v", reports)
	}
	if reports := feed.VolcanicReports(time.Time{}, feed.start.Add(-time.Nanosecond)); len(reports) != 0 {
		t.Fatalf("reports after now returned: %+v", reports)
	}
}

// The optional confidence field must be absent from JSON until enabled.
func TestOriginalSchemaOmitsConfidence(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	reports := feed.VolcanicReports(time.Time{}, start)
	before, err := json.Marshal(reports[0])
	if err != nil || strings.Contains(string(before), "confidence_level") {
		t.Fatalf("optional field must be absent: %s, %v", before, err)
	}
}

// Schema toggles add/remove confidence without replacing the feed.
func TestConfidenceCanBeEnabledAndDisabled(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	now := start
	feed.SetConfidence(true)
	reports := feed.VolcanicReports(time.Time{}, now)
	if reports[0].ConfidenceLevel == nil || *reports[0].ConfidenceLevel != 0.85 {
		t.Fatal("new schema attribute missing")
	}
	feed.SetConfidence(false)
	if feed.VolcanicReports(time.Time{}, now)[0].ConfidenceLevel != nil {
		t.Fatal("schema toggle did not reset")
	}
}

// The since filter includes reports exactly at the requested timestamp.
func TestVolcanicSinceIncludesBoundary(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	now := start.Add(2 * time.Second)
	reports := feed.VolcanicReports(start.Add(time.Second), now)
	if len(reports) != 2 || !reports[0].ReportedAt.Equal(start.Add(time.Second)) {
		t.Fatalf("inclusive timeline filtering: %+v", reports)
	}
}

// An empty result must serialize as [] rather than null.
func TestEmptyReportsReturnArray(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	now := start
	if reports := feed.VolcanicReports(now.Add(time.Second), now); reports == nil || len(reports) != 0 {
		t.Fatal("empty response must encode as an array")
	}
}

// Outage state can be toggled and read independently of HTTP.
func TestOutageCanBeEnabledAndCleared(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, time.Second)
	feed.SetOutage(true)
	if !feed.IsOutage() || !feed.State().Outage {
		t.Fatal("outage not enabled")
	}
	feed.SetOutage(false)
	if feed.IsOutage() {
		t.Fatal("outage not cleared")
	}
}

// Restarting at a later time preserves every overlapping time bucket.
func TestVolcanicTimelineIsDeterministicAcrossRestarts(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	restarted := start.Add(2 * time.Second)
	now := restarted.Add(2 * time.Second)

	before := NewFeed(start, time.Second).VolcanicReports(restarted, now)
	after := NewFeed(restarted, time.Second).VolcanicReports(restarted, now)
	if len(before) != len(after) {
		t.Fatalf("overlap length changed after restart: %d != %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("bucket changed after restart: %+v != %+v", before[i], after[i])
		}
	}
}
