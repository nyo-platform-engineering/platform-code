package database

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// authSchemaLock identifies schema changes shared by all backend replicas.
const authSchemaLock = 748239015

// Migrate synchronizes only the PostgreSQL auth models. The transaction lock
// serializes schema changes when multiple backend replicas start together.
func Migrate(ctx context.Context, db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return errors.New("auth schema migration requires PostgreSQL")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", authSchemaLock).Error; err != nil {
			return fmt.Errorf("lock PostgreSQL auth schema: %w", err)
		}
		if err := tx.AutoMigrate(&LoginAttempt{}, &Session{}); err != nil {
			return fmt.Errorf("synchronize PostgreSQL auth schema: %w", err)
		}
		return CheckSchema(ctx, tx)
	})
}
