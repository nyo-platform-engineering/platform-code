package storage

import (
	"context"
	"sort"
	"time"

	"aat/internal/analytics"
)

type AnalyticsReader interface {
	Analytics(context.Context, analytics.Query) (analytics.Result, error)
}

// One SQL statement supplies every chart from the same read. Citus handles
// aggregation across canonical shards; no consumer-side copy can drift from audit.
// Tsunami is a subset of seismic events with an actual mapped warning, not merely
// potential_tsunami=true. Severity/area/total count each canonical event once.
const analyticsSQL = `WITH base AS MATERIALIZED (
 SELECT hazard_type, severity, area_name, occurred_at,
 CASE WHEN jsonb_typeof(attributes->'tsunami_warnings') = 'array'
 THEN jsonb_array_length(attributes->'tsunami_warnings') > 0 ELSE false END AS tsunami
 FROM hazard_events WHERE occurred_at >= ? AND occurred_at < ? AND (? = '' OR source = ?)
), classified AS (
 SELECT occurred_at, unnest(CASE WHEN hazard_type = 'SEISMIC' AND tsunami
 THEN ARRAY['SEISMIC','TSUNAMI'] ELSE ARRAY[hazard_type] END) AS kind FROM base
)
SELECT 'total' AS dimension, '' AS key, NULL::timestamptz AS at, count(*) AS count FROM base
UNION ALL SELECT 'severity', severity, NULL::timestamptz, count(*) FROM base GROUP BY severity
UNION ALL SELECT 'area', area_name, NULL::timestamptz, count(*) FROM base GROUP BY area_name
UNION ALL SELECT 'type', kind, NULL::timestamptz, count(*) FROM classified GROUP BY kind
UNION ALL SELECT 'time', kind, date_trunc(?::text,occurred_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',count(*)
 FROM classified GROUP BY 2,3`

func (p *Postgres) Analytics(ctx context.Context, q analytics.Query) (analytics.Result, error) {
	result := analytics.Result{Query: q, AsOf: time.Now().UTC(), ByType: []analytics.Count{}, BySeverity: []analytics.Count{}, ByArea: []analytics.Count{}, Timeline: []analytics.Bucket{}, UnavailableUnits: []string{}}
	if err := q.Validate(); err != nil {
		return result, err
	}
	var rows []struct {
		Dimension, Key string
		At             *time.Time
		Count          int64
	}
	interval := q.Interval
	if err := p.db.WithContext(ctx).Raw(analyticsSQL, q.From, q.To, q.Source, q.Source, interval).Scan(&rows).Error; err != nil {
		result.Partial = true
		result.UnavailableUnits = []string{p.identity.Unit}
		return result, nil
	}
	total := int64(0)
	result.Total = &total
	types := map[string]int64{}
	severity := map[string]int64{}
	timeline := map[time.Time]map[string]int64{}
	for _, r := range rows {
		switch r.Dimension {
		case "total":
			total = r.Count
		case "type":
			types[r.Key] = r.Count
		case "severity":
			severity[r.Key] = r.Count
		case "area":
			result.ByArea = append(result.ByArea, analytics.Count{Key: r.Key, Count: r.Count})
		case "time":
			at := r.At.UTC()
			if timeline[at] == nil {
				timeline[at] = map[string]int64{}
			}
			timeline[at][r.Key] = r.Count
		}
	}
	for _, key := range analytics.Types {
		result.ByType = append(result.ByType, analytics.Count{Key: key, Count: types[key]})
	}
	for _, key := range analytics.Severities {
		result.BySeverity = append(result.BySeverity, analytics.Count{Key: key, Count: severity[key]})
	}
	for at := analytics.BucketStart(q.From, q.Interval); at.Before(q.To); at = analytics.NextBucket(at, q.Interval) {
		counts := map[string]int64{}
		for _, key := range analytics.Types {
			counts[key] = timeline[at][key]
		}
		result.Timeline = append(result.Timeline, analytics.Bucket{At: at, Counts: counts})
	}
	sort.Slice(result.ByArea, func(i, j int) bool {
		if result.ByArea[i].Count == result.ByArea[j].Count {
			return result.ByArea[i].Key < result.ByArea[j].Key
		}
		return result.ByArea[i].Count > result.ByArea[j].Count
	})
	return result, nil
}

func (c *Citus) Analytics(ctx context.Context, q analytics.Query) (analytics.Result, error) {
	r, err := c.Postgres.Analytics(ctx, q)
	if err == nil && r.Partial {
		a := c.unavailable(ctx)
		r.UnavailableUnits = a.UnavailableUnits
	}
	return r, err
}
