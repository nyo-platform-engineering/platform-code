# Database

Owns the PostgreSQL connection and auth schema. ClickHouse connections and queries
live separately in `internal/query`; they never run AutoMigrate.

- `postgres.go` opens GORM, bounds the underlying pool, and disables SQL logging.
  `main` closes the pool on shutdown.
- `models.go` defines the auth tables, required columns, primary keys, and expiry
  indexes. Auth storage uses these same models for its queries.
- `schema.go` checks required column types, NOT NULL constraints, the primary key,
  and valid expiry indexes against the models. A mismatch blocks startup.
- `migrate.go` runs GORM AutoMigrate before the OAuth server starts. A transaction
  and PostgreSQL advisory lock serialize concurrent replica startup. Schema validation
  runs in the same transaction before it commits.

The PostgreSQL role needs permission to create and alter these tables and indexes,
as well as read, insert, and delete records. `go run . -migrate` optionally runs
the same schema synchronization and exits. There are no SQL migration files or
migration ledger checks. Existing ledger tables are left unused.
