package aggregate

// Repeated ingestion preserves ingested_at unless the actual event changed.
const upsertHazard = `
INSERT INTO hazards (
    id, source, source_ref_id, hazard_type, severity, area_name,
    latitude, longitude, occurred_at, ingested_at, attributes
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO UPDATE SET
    source = excluded.source,
    source_ref_id = excluded.source_ref_id,
    hazard_type = excluded.hazard_type,
    severity = excluded.severity,
    area_name = excluded.area_name,
    latitude = excluded.latitude,
    longitude = excluded.longitude,
    occurred_at = excluded.occurred_at,
    ingested_at = excluded.ingested_at,
    attributes = excluded.attributes
WHERE ROW(
    hazards.source,
    hazards.source_ref_id,
    hazards.hazard_type,
    hazards.severity,
    hazards.area_name,
    hazards.latitude,
    hazards.longitude,
    hazards.occurred_at,
    hazards.attributes
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
