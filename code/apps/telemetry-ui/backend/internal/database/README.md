# Database

Owns the PostgreSQL control-plane connection and schema: sessions, organizations,
access grants, permissions, datasource metadata, and signal assignments.
ClickHouse connections and queries live separately in `internal/query`.

- `postgres.go` opens GORM, bounds the underlying pool, and disables SQL logging.
  `main` closes the pool on shutdown.
- `models.go` defines the control-plane tables, required columns, primary keys,
  and indexes. Runtime storage uses these same models for its queries.
- `schema.go` checks required column types, NOT NULL constraints, the primary key,
  and valid expiry indexes against the models. A mismatch blocks startup.
- `migrate.go` runs GORM AutoMigrate before the OAuth server starts. A transaction
  and PostgreSQL advisory lock serialize concurrent replica startup. Schema validation
  runs in the same transaction before it commits. The migration expands the signal
  assignment primary key to `(organization_id, signal, data_source_id)` so one signal
  can expose multiple selectable datasources.

The PostgreSQL role needs permission to create and alter these tables and indexes,
as well as read, insert, update, and delete records. `go run . -migrate` optionally runs
the same schema synchronization and exits. There are no SQL migration files or
migration ledger checks. Existing ledger tables are left unused.
