package database

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// schemaLock identifies control-plane schema changes shared by all replicas.
const schemaLock = 748239015

// Migrate synchronizes the PostgreSQL control-plane models. The transaction lock
// serializes schema changes when multiple backend replicas start together.
func Migrate(ctx context.Context, db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return errors.New("control-plane schema migration requires PostgreSQL")
	}
	return WithControlLock(ctx, db, func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(Models()...); err != nil {
			return fmt.Errorf("synchronize PostgreSQL control-plane schema: %w", err)
		}
		// Earlier releases allowed only one datasource for an organization and
		// signal. Expand that primary key in place before validating the schema.
		if err := tx.Exec(`
DO $$
DECLARE assignment_primary_key text;
DECLARE assignment_primary_key_columns integer;
BEGIN
  SELECT constraint_definition.conname, array_length(constraint_definition.conkey, 1)
    INTO assignment_primary_key, assignment_primary_key_columns
  FROM pg_constraint AS constraint_definition
  WHERE constraint_definition.conrelid = to_regclass('telemetry_organization_data_sources')
    AND constraint_definition.contype = 'p';
  IF assignment_primary_key_columns = 2 THEN
    EXECUTE format('ALTER TABLE telemetry_organization_data_sources DROP CONSTRAINT %I', assignment_primary_key);
    ALTER TABLE telemetry_organization_data_sources
      ADD PRIMARY KEY (organization_id, signal, data_source_id);
  END IF;
END $$`).Error; err != nil {
			return fmt.Errorf("expand datasource assignment primary key: %w", err)
		}
		return CheckSchema(ctx, tx)
	})
}

// WithControlLock serializes migrations and GitOps bootstrap reconciliation
// across replicas using one transaction-scoped PostgreSQL advisory lock.
func WithControlLock(ctx context.Context, db *gorm.DB, apply func(*gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", schemaLock).Error; err != nil {
			return fmt.Errorf("lock PostgreSQL control plane: %w", err)
		}
		return apply(tx)
	})
}
