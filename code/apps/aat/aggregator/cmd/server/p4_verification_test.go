package main

import (
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
	"time"
)

func TestP4VerificationSearchPathCannotTargetLiveSchema(t *testing.T) {
	for _, schema := range []string{"public", "public,pg_catalog", "p4_verify_other_baseline", "p4_verify_012345abcdef_baseline;DROP SCHEMA public"} {
		cfg := &pgx.ConnConfig{}
		if configureP4Verification(cfg, schema) == nil {
			t.Fatalf("accepted unsafe schema %q", schema)
		}
	}
	cfg := &pgx.ConnConfig{}
	if err := configureP4Verification(cfg, "p4_verify_012345abcdef_baseline"); err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeParams["search_path"] != "p4_verify_012345abcdef_baseline,public" {
		t.Fatal("missing schema isolation")
	}
	if err := configureP4Verification(&pgx.ConnConfig{}, ""); err != nil {
		t.Fatal(err)
	}
}

// Developer integration check uses a schema prepared by the cluster runner,
// without switching the running deployment or writing live data.
func TestP4VerificationPreparedClusterSchema(t *testing.T) {
	url := os.Getenv("AAT_P4_SCHEMA_TEST_URL")
	schema := os.Getenv("AAT_P4_SCHEMA_TEST_NAME")
	if url == "" || schema == "" {
		t.Skip("set AAT_P4_SCHEMA_TEST_URL and AAT_P4_SCHEMA_TEST_NAME")
	}
	if !p4SchemaName.MatchString(schema) {
		t.Fatal("invalid owned verification schema")
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid schema test connection")
	}
	if err = configureP4Verification(config, schema); err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err = validateP4Verification(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	if err = model.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = storage.EnableCitus(ctx, db); err != nil {
		t.Fatal(err)
	}
	var distributed bool
	if err = db.Raw(`SELECT EXISTS(SELECT 1 FROM pg_dist_partition WHERE logicalrelid='hazard_events'::regclass AND partmethod='h')`).Scan(&distributed).Error; err != nil || !distributed {
		t.Fatalf("verification table is not distributed: %v %v", distributed, err)
	}
	// The private schema must never use a production serial sequence.
	var sequenceSchemas []string
	query := `SELECT sn.nspname FROM pg_attrdef ad
 JOIN pg_class t ON t.oid=ad.adrelid JOIN pg_namespace tn ON tn.oid=t.relnamespace
 JOIN pg_depend d ON d.classid='pg_attrdef'::regclass AND d.objid=ad.oid
 JOIN pg_class seq ON seq.oid=d.refobjid AND seq.relkind='S'
 JOIN pg_namespace sn ON sn.oid=seq.relnamespace WHERE tn.nspname=?`
	if err = db.Raw(query, schema).Scan(&sequenceSchemas).Error; err != nil {
		t.Fatal(err)
	}
	for _, value := range sequenceSchemas {
		if value != schema {
			t.Fatalf("sequence escaped schema: %s", value)
		}
	}

}
