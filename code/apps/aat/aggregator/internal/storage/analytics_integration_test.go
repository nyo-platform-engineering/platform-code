package storage

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"aat/aggregator/internal/model"
	"aat/internal/analytics"
	"gorm.io/gorm"
)

func TestAnalyticsMatchesCanonicalEvents(t *testing.T) {
	db := testPostgres(t)
	ctx := context.Background()
	if os.Getenv("AAT_TEST_CITUS") == "1" {
		if err := EnableCitus(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	when := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	fixture := []struct {
		kind, source, severity, area string
		warnings                     string
	}{
		{"SEISMIC", "BMKG", "NORMAL", "Jawa", ""},
		{"SEISMIC", "BMKG", "AWAS", "Jawa", `[{"warning_id":"w1"}]`},
		{"SEISMIC", "BMKG", "SIAGA", "Bali", `[]`},
		{"VOLCANIC", "PVMBG", "SIAGA", "Jawa", ""},
		{"WILDFIRE", "SATELIT", "WASPADA", "Kalimantan", ""},
		{"COASTAL", "SENSOR_PANTAI", "AWAS", "Bali", ""},
	}
	for i, f := range fixture {
		attrs := model.Record{"potential_tsunami": json.RawMessage("true")}
		if f.warnings != "" {
			attrs["tsunami_warnings"] = json.RawMessage(f.warnings)
		}
		h := model.HazardEvent{ID: string(rune('a' + i)), Ref: string(rune('a' + i)), Source: f.source, Type: f.kind, Severity: f.severity, Area: f.area, Occurred: when.Add(time.Duration(i) * time.Hour), Ingested: when, Attributes: attrs}
		if err := db.Create(&h).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := analytics.Query{From: when, To: when.Add(6 * time.Hour), Interval: "hour"}
	store := NewPostgres(db, Identity{Unit: "test"})
	result, err := store.Analytics(ctx, q)
	if err != nil || result.Partial || result.Validate() != nil || result.Total == nil || *result.Total != 6 {
		t.Fatalf("analytics: %+v %v validation=%v", result, err, result.Validate())
	}
	counts := map[string]int64{}
	for _, n := range result.ByType {
		counts[n.Key] = n.Count
	}
	if counts["SEISMIC"] != 3 || counts["TSUNAMI"] != 1 || counts["VOLCANIC"] != 1 || counts["WILDFIRE"] != 1 || counts["COASTAL"] != 1 {
		t.Fatalf("classification: %v", counts)
	}
	page, err := store.Events(ctx, EventQuery{Limit: 100})
	if err != nil || len(page.Items) != int(*result.Total) {
		t.Fatalf("audit mismatch: %+v %v", page, err)
	}
	expectedSeverity := map[string]int64{}
	expectedArea := map[string]int64{}
	for _, h := range page.Items {
		expectedSeverity[h.Severity]++
		expectedArea[h.Area]++
	}
	for _, c := range result.BySeverity {
		if c.Count != expectedSeverity[c.Key] {
			t.Fatal("severity disagrees with audit")
		}
	}
	for _, c := range result.ByArea {
		if c.Count != expectedArea[c.Key] {
			t.Fatal("area disagrees with audit")
		}
	}
	q.Source = "BMKG"
	filtered, err := store.Analytics(ctx, q)
	if err != nil || filtered.Validate() != nil || *filtered.Total != 3 {
		t.Fatalf("source filter: %+v %v", filtered, err)
	}
	// The end boundary is exclusive, and empty buckets remain visible.
	q.Source = ""
	q.To = when.Add(3 * time.Hour)
	rangeResult, err := store.Analytics(ctx, q)
	if err != nil || rangeResult.Validate() != nil || *rangeResult.Total != 3 || len(rangeResult.Timeline) != 3 {
		t.Fatalf("range boundary: %+v %v", rangeResult, err)
	}
	q.From = when.Add(24 * time.Hour)
	q.To = q.From.Add(time.Hour)
	empty, err := store.Analytics(ctx, q)
	if err != nil || empty.Validate() != nil || *empty.Total != 0 || len(empty.ByType) != 5 {
		t.Fatalf("empty is not unavailable: %+v %v", empty, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	partial, err := store.Analytics(canceled, q)
	if err != nil || !partial.Partial || partial.Total != nil || len(partial.Timeline) != 0 || partial.Validate() != nil {
		t.Fatalf("unavailable must not show zeros: %+v %v", partial, err)
	}
	q.From = when
	q.To = when.Add(72 * time.Hour)
	q.Interval = "week"
	weekly, err := store.Analytics(ctx, q)
	if err != nil || weekly.Validate() != nil || *weekly.Total != 6 {
		t.Fatalf("weekly buckets: %+v %v", weekly, err)
	}
	for _, interval := range []string{"month", "year"} {
		q.From = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		q.To = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		q.Interval = interval
		historical, err := store.Analytics(ctx, q)
		if err != nil || historical.Validate() != nil || *historical.Total != 6 {
			t.Fatalf("historical %s: %+v %v", interval, historical, err)
		}
	}
	q.From = when.Truncate(24 * time.Hour)
	q.To = q.From.Add(24 * time.Hour)
	q.Interval = "day"
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL TIME ZONE 'Asia/Jakarta'").Error; err != nil {
			return err
		}
		r, err := NewPostgres(tx, Identity{}).Analytics(ctx, q)
		if err != nil {
			return err
		}
		if r.Validate() != nil || !r.Timeline[0].At.Equal(q.From) {
			t.Fatal("UTC buckets changed with database session timezone")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

}
