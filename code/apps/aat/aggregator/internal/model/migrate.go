package model

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

var retiredConstraints = []string{"hazards_source_check", "hazards_hazard_type_check"}

// Serialize startup migrations across aggregator replicas.
func Migrate(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(403100)").Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL statement_timeout = '60s'").Error; err != nil {
			return err
		}
		// ALTER TABLE locks the table even when nothing is dropped, so only run it for checks that still exist.
		var present []string
		if err := tx.Raw("SELECT conname FROM pg_constraint WHERE conrelid = to_regclass('hazard_events') AND conname IN ?", retiredConstraints).
			Scan(&present).Error; err != nil {
			return err
		}
		for _, name := range present {
			if err := tx.Exec("ALTER TABLE hazard_events DROP CONSTRAINT " + name).Error; err != nil {
				return err
			}
		}
		// Citus requires every unique key to contain the distribution column.
		// Preserve existing hazard IDs while allowing distribution by source_ref_id.
		var primary string
		if err := tx.Raw(`SELECT conname FROM pg_constraint WHERE conrelid = to_regclass('hazard_events') AND contype = 'p' AND NOT (pg_get_constraintdef(oid) LIKE '%source_ref_id%')`).Scan(&primary).Error; err != nil {
			return err
		}
		if primary != "" {
			// Quote catalog-derived names rather than interpolating SQL identifiers.
			if err := tx.Exec(`ALTER TABLE hazard_events DROP CONSTRAINT "` + strings.ReplaceAll(primary, `"`, `""`) + `", ADD PRIMARY KEY (id, source_ref_id)`).Error; err != nil {
				return err
			}
		}
		return tx.AutoMigrate(&SeismicEvent{}, &TsunamiWarning{}, &HazardEvent{}, &PollState{}, &PollOwner{}, &HazardOutbox{}, &SatelliteDetection{}, &SatelliteFire{}, &SatelliteFeed{}, &SatelliteReject{})
	})
}
