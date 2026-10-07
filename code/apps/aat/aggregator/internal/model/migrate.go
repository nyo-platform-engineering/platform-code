package model

import (
	"context"

	"gorm.io/gorm"
)

// Serialize startup migrations across aggregator replicas.
func Migrate(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(403100)").Error; err != nil {
			return err
		}
		return tx.AutoMigrate(&SeismicEvent{}, &TsunamiWarning{}, &HazardEvent{}, &PollState{}, &HazardOutbox{})
	})
}
