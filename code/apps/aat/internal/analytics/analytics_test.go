package analytics

import (
	"net/url"
	"testing"
	"time"
)

func TestQueryBoundsAndTimezones(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, raw := range []string{"from=wrong", "from=2026-11-19T00:00:00Z&to=2026-11-18T00:00:00Z", "interval=minute", "source=OTHER", "source=BMKG&source=PVMBG", "extra=value", "from=2026-01-01T00:00:00Z&interval=hour"} {
		values, _ := url.ParseQuery(raw)
		if _, err := Parse(values, now); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	q, err := Parse(url.Values{"from": {"2026-11-18T12:00:00+07:00"}, "to": {"2026-11-18T13:00:00+07:00"}}, now)
	if err != nil || q.From.Hour() != 5 || q.From.Location() != time.UTC || q.Interval != "hour" {
		t.Fatalf("timezone: %+v %v", q, err)
	}
	if _, err := Parse(q.Values(), now); err != nil {
		t.Fatal("query roundtrip:", err)
	}
}

func TestCalendarBucketsAndHistoricalRanges(t *testing.T) {
	leap := time.Date(2024, 2, 29, 23, 0, 0, 0, time.UTC)
	if got := NextBucket(BucketStart(leap, "month"), "month"); !got.Equal(time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("month must follow calendar boundaries:", got)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	q, err := Parse(url.Values{"from": {"1900-01-01T00:00:00Z"}, "to": {now.Format(time.RFC3339)}}, now)
	if err != nil || q.Interval != "year" {
		t.Fatalf("historical catalog range: %+v %v", q, err)
	}
	q, err = Parse(url.Values{"from": {"2020-01-01T00:00:00Z"}}, now)
	if err != nil || q.Interval != "month" {
		t.Fatalf("multi-year range: %+v %v", q, err)
	}
}
