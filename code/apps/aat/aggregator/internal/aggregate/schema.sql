-- Preserve original source payloads, including unknown fields, for remapping.
CREATE TABLE IF NOT EXISTS raw_seismic (
    id text PRIMARY KEY,
    document jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS tsunami_warnings (
    id text PRIMARY KEY,
    event_id text NOT NULL,
    document jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS warnings_event ON tsunami_warnings(event_id);

-- Standardized fields have typed columns; additive attributes stay dynamic.
CREATE TABLE IF NOT EXISTS hazards (
    id text PRIMARY KEY,
    source text NOT NULL CHECK (source IN ('BMKG', 'PVMBG')),
    source_ref_id text NOT NULL,
    hazard_type text NOT NULL CHECK (hazard_type IN ('SEISMIC', 'VOLCANIC')),
    severity text NOT NULL CHECK (severity IN ('NORMAL', 'WASPADA', 'SIAGA', 'AWAS')),
    area_name text NOT NULL,
    latitude double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    occurred_at timestamptz NOT NULL,
    ingested_at timestamptz NOT NULL,
    attributes jsonb NOT NULL CHECK (jsonb_typeof(attributes) = 'object')
);

-- Upgrade existing data atomically under the startup advisory lock.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'hazards' AND column_name = 'document'
    ) THEN
        ALTER TABLE hazards
            ADD COLUMN source_ref_id text,
            ADD COLUMN hazard_type text,
            ADD COLUMN severity text,
            ADD COLUMN area_name text,
            ADD COLUMN latitude double precision,
            ADD COLUMN longitude double precision,
            ADD COLUMN occurred_at timestamptz,
            ADD COLUMN ingested_at timestamptz,
            ADD COLUMN attributes jsonb;

        UPDATE hazards SET
            source_ref_id = (document->>'source_ref_id')::text,
            hazard_type = (document->>'hazard_type')::text,
            severity = (document->>'severity')::text,
            area_name = (document->>'area_name')::text,
            latitude = (document->>'latitude')::double precision,
            longitude = (document->>'longitude')::double precision,
            occurred_at = (document->>'occurred_at')::timestamptz,
            ingested_at = (document->>'ingested_at')::timestamptz,
            attributes = document->'attributes';

        ALTER TABLE hazards
            ALTER COLUMN source_ref_id SET NOT NULL,
            ALTER COLUMN hazard_type SET NOT NULL,
            ALTER COLUMN severity SET NOT NULL,
            ALTER COLUMN area_name SET NOT NULL,
            ALTER COLUMN latitude SET NOT NULL,
            ALTER COLUMN longitude SET NOT NULL,
            ALTER COLUMN occurred_at SET NOT NULL,
            ALTER COLUMN ingested_at SET NOT NULL,
            ALTER COLUMN attributes SET NOT NULL,
            ADD CHECK (source IN ('BMKG', 'PVMBG')),
            ADD CHECK (hazard_type IN ('SEISMIC', 'VOLCANIC')),
            ADD CHECK (severity IN ('NORMAL', 'WASPADA', 'SIAGA', 'AWAS')),
            ADD CHECK (latitude BETWEEN -90 AND 90),
            ADD CHECK (longitude BETWEEN -180 AND 180),
            ADD CHECK (jsonb_typeof(attributes) = 'object'),
            DROP COLUMN document;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS hazards_source ON hazards(source, id);
CREATE UNIQUE INDEX IF NOT EXISTS hazards_source_ref ON hazards(source, source_ref_id);
