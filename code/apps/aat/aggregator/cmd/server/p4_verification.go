package main

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

var p4SchemaName = regexp.MustCompile(`^p4_verify_[a-f0-9]{12}_(baseline|crash)$`)

// Verification uses the existing database, but must never fall through to live
// tables through search_path. The runner prepares empty copies before rollout.
var p4Tables = []string{"seismic_events", "tsunami_warnings", "hazard_events", "poll_states", "poll_owners",
	"inbox_messages", "field_report_submissions", "field_reports", "hazard_outboxes", "satellite_detections",
	"satellite_fires", "satellite_feeds", "satellite_rejects", "sensor_stations", "sensor_states", "hazard_read_revision"}

func configureP4Verification(config *pgx.ConnConfig, schema string) error {
	if schema == "" {
		return nil
	}
	if !p4SchemaName.MatchString(schema) {
		return errors.New("invalid P4_TEST_SCHEMA")
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = map[string]string{}
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	return nil
}
func validateP4Verification(ctx context.Context, db *gorm.DB, schema string) error {
	if schema == "" {
		return nil
	}
	if !p4SchemaName.MatchString(schema) {
		return errors.New("invalid P4_TEST_SCHEMA")
	}
	for _, table := range p4Tables {
		var namespace string
		if err := db.WithContext(ctx).Raw(`SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=to_regclass(?)`, table).Scan(&namespace).Error; err != nil {
			return err
		}
		if namespace != schema {
			return errors.New("P4 verification table missing or resolves to live schema: " + table)
		}
	}
	return nil
}
