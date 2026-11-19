package storage

const recentPageSQL = `WITH latest AS MATERIALIZED (
 SELECT id, source_ref_id, occurred_at FROM hazard_events
 WHERE (? = '' OR source = ?) AND (? OR (occurred_at, id) < (?, ?))
 ORDER BY occurred_at DESC, id DESC LIMIT ?
)
SELECT h.* FROM hazard_events h JOIN latest l
 ON h.source_ref_id = l.source_ref_id AND h.id = l.id
ORDER BY h.occurred_at DESC, h.id DESC`

// Repeated ingestion preserves ingested_at unless the actual event changed.
const hazardChanged = `ROW(
    hazard_events.source,
    hazard_events.source_ref_id,
    hazard_events.hazard_type,
    hazard_events.severity,
    hazard_events.area_name,
    hazard_events.latitude,
    hazard_events.longitude,
    hazard_events.occurred_at,
    hazard_events.attributes
) IS DISTINCT FROM ROW(
    excluded.source,
    excluded.source_ref_id,
    excluded.hazard_type,
    excluded.severity,
    excluded.area_name,
    excluded.latitude,
    excluded.longitude,
    excluded.occurred_at,
    excluded.attributes
)
`
