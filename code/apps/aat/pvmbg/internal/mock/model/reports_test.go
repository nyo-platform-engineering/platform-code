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
	feed := NewFeed(start, "test", time.Second)
	reports := feed.VolcanicReports(time.Time{}, start)
	if len(reports) != 20 {
		t.Fatalf("seed count: %d", len(reports))
	}
}

// The optional confidence field must be absent from JSON until enabled.
func TestOriginalSchemaOmitsConfidence(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)
	reports := feed.VolcanicReports(time.Time{}, start)
	before, err := json.Marshal(reports[0])
	if err != nil || strings.Contains(string(before), "confidence_level") {
		t.Fatalf("optional field must be absent: %s, %v", before, err)
	}
}

// Schema toggles add/remove confidence without replacing the feed.
func TestConfidenceCanBeEnabledAndDisabled(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)
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
	feed := NewFeed(start, "test", time.Second)
	now := start.Add(2 * time.Second)
	reports := feed.VolcanicReports(start.Add(time.Second), now)
	if len(reports) != 2 || !reports[0].ReportedAt.Equal(start.Add(time.Second)) {
		t.Fatalf("inclusive timeline filtering: %+v", reports)
	}
}

// An empty result must serialize as [] rather than null.
func TestEmptyReportsReturnArray(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)
	now := start
	if reports := feed.VolcanicReports(now.Add(time.Second), now); reports == nil || len(reports) != 0 {
		t.Fatal("empty response must encode as an array")
	}
}

// Outage state can be toggled and read independently of HTTP.
func TestOutageCanBeEnabledAndCleared(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	feed := NewFeed(start, "test", time.Second)
	feed.SetOutage(true)
	if !feed.IsOutage() || !feed.State().Outage {
		t.Fatal("outage not enabled")
	}
	feed.SetOutage(false)
	if feed.IsOutage() {
		t.Fatal("outage not cleared")
	}
}
