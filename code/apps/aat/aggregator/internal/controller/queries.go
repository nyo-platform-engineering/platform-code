package controller

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
