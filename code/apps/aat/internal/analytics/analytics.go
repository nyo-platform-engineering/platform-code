// Package analytics defines the read-only dashboard contract shared by the API
// and canonical store. All timestamps and bucket boundaries use UTC.
package analytics

import (
	"errors"
	"net/url"
	"slices"
	"time"
)

var Types = []string{"SEISMIC", "TSUNAMI", "VOLCANIC", "WILDFIRE", "COASTAL"}
var Severities = []string{"NORMAL", "WASPADA", "SIAGA", "AWAS"}

type Query struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Interval string    `json:"interval"`
	Source   string    `json:"source,omitempty"`
}

func BucketStart(at time.Time, interval string) time.Time {
	at = at.UTC()
	switch interval {
	case "hour":
		return at.Truncate(time.Hour)
	case "day":
		return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	case "week":
		return at.Truncate(7 * 24 * time.Hour)
	case "month":
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "year":
		return time.Date(at.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Time{}
}

func NextBucket(at time.Time, interval string) time.Time {
	switch interval {
	case "hour":
		return at.Add(time.Hour)
	case "day":
		return at.AddDate(0, 0, 1)
	case "week":
		return at.AddDate(0, 0, 7)
	case "month":
		return at.AddDate(0, 1, 0)
	case "year":
		return at.AddDate(1, 0, 0)
	}
	return time.Time{}
}

func (q Query) Validate() error {
	if !slices.Contains([]string{"hour", "day", "week", "month", "year"}, q.Interval) || q.From.IsZero() || q.From.Year() < 1 || !q.To.After(q.From) {
		return errors.New("invalid range or interval (hour/day/week/month/year)")
	}
	count := 0
	for at := BucketStart(q.From, q.Interval); at.Before(q.To); at = NextBucket(at, q.Interval) {
		count++
		if count > 1000 {
			return errors.New("range exceeds 1000 buckets; choose a wider interval")
		}
	}
	if q.Source != "" && !slices.Contains([]string{"BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI"}, q.Source) {
		return errors.New("invalid source")
	}
	return nil
}

func Parse(values url.Values, now time.Time) (Query, error) {
	q := Query{From: now.UTC().Add(-24 * time.Hour), To: now.UTC(), Interval: "hour", Source: values.Get("source")}
	for key, v := range values {
		if len(v) != 1 || !slices.Contains([]string{"from", "to", "interval", "source"}, key) {
			return q, errors.New("use from, to, interval, and source once each")
		}
	}
	var err error
	if values.Has("from") {
		q.From, err = time.Parse(time.RFC3339Nano, values.Get("from"))
		if err != nil {
			return q, errors.New("from must be an ISO 8601 timestamp")
		}
	}
	if values.Has("to") {
		q.To, err = time.Parse(time.RFC3339Nano, values.Get("to"))
		if err != nil {
			return q, errors.New("to must be an ISO 8601 timestamp")
		}
	}
	if values.Has("interval") {
		q.Interval = values.Get("interval")
	} else {
		switch span := q.To.Sub(q.From); {
		case span > 20*365*24*time.Hour:
			q.Interval = "year"
		case span > 366*24*time.Hour:
			q.Interval = "month"
		case span > 72*time.Hour:
			q.Interval = "day"
		}
	}
	q.From, q.To = q.From.UTC(), q.To.UTC()
	return q, q.Validate()
}

func (q Query) Values() url.Values {
	return url.Values{"from": {q.From.Format(time.RFC3339Nano)}, "to": {q.To.Format(time.RFC3339Nano)}, "interval": {q.Interval}, "source": {q.Source}}
}

type Count struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}
type Bucket struct {
	At     time.Time        `json:"at"`
	Counts map[string]int64 `json:"counts"`
}
type Result struct {
	Query
	AsOf             time.Time `json:"as_of"`
	Total            *int64    `json:"total"`
	ByType           []Count   `json:"by_type"`
	BySeverity       []Count   `json:"by_severity"`
	ByArea           []Count   `json:"by_area"`
	Timeline         []Bucket  `json:"timeline"`
	Partial          bool      `json:"partial"`
	UnavailableUnits []string  `json:"unavailable_units"`
}

// Validate checks chart totals before an upstream response reaches the browser.
// Tsunami is an overlapping seismic subset; all other dimensions count unique events.
func (r Result) Validate() error {
	invalid := errors.New("invalid analytics response")
	if r.Query.Validate() != nil || r.AsOf.IsZero() || r.UnavailableUnits == nil {
		return invalid
	}
	if r.Partial {
		if r.Total != nil || len(r.UnavailableUnits) == 0 {
			return invalid
		}
		return nil
	}
	if r.Total == nil || *r.Total < 0 || len(r.UnavailableUnits) != 0 {
		return invalid
	}
	check := func(items []Count, allowed []string) (map[string]int64, bool) {
		counts := map[string]int64{}
		for _, item := range items {
			if _, ok := counts[item.Key]; ok || item.Count < 0 || (allowed != nil && !slices.Contains(allowed, item.Key)) {
				return nil, false
			}
			counts[item.Key] = item.Count
		}
		if allowed != nil && len(counts) != len(allowed) {
			return nil, false
		}
		return counts, true
	}
	types, ok := check(r.ByType, Types)
	if !ok {
		return invalid
	}
	severity, ok := check(r.BySeverity, Severities)
	if !ok {
		return invalid
	}
	areas, ok := check(r.ByArea, nil)
	if !ok {
		return invalid
	}
	var canonical int64
	for k, n := range types {
		if k != "TSUNAMI" {
			canonical += n
		}
	}
	if canonical != *r.Total || types["TSUNAMI"] > types["SEISMIC"] {
		return invalid
	}
	for _, dimension := range []map[string]int64{severity, areas} {
		var n int64
		for _, c := range dimension {
			n += c
		}
		if n != *r.Total {
			return invalid
		}
	}
	totals := map[string]int64{}
	at := BucketStart(r.From, r.Interval)
	for _, bucket := range r.Timeline {
		if !bucket.At.Equal(at) || !at.Before(r.To) || len(bucket.Counts) != len(Types) {
			return invalid
		}
		for _, kind := range Types {
			n, ok := bucket.Counts[kind]
			if !ok || n < 0 {
				return invalid
			}
			totals[kind] += n
		}
		at = NextBucket(at, r.Interval)
	}
	if at.Before(r.To) {
		return invalid
	}
	for _, kind := range Types {
		if totals[kind] != types[kind] {
			return invalid
		}
	}
	return nil
}
