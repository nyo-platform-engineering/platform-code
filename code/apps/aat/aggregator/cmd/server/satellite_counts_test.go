package main

import (
	"context"
	"os"
	"testing"

	"aat/aggregator/internal/controller"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The cluster sets statement_timeout = 2 s for every session. The stats counts scan
// every detection, which took 2.5 s at 1.7M detections, so /internal/satellite/stats
// answered 503 forever. Here the sessions default to 5 ms over 300k detections: the
// counts must still answer, exactly, within their own budget.
func TestSatelliteCountsOutlastTheSessionStatementTimeout(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	fixtures := []string{
		// 100,000 fires of 3 detections each.
		`INSERT INTO satellite_detections (detection_key, satellite, detected_at, latitude, longitude,
		   confidence, frp_mw, late, retracted, fire_ref, received_at)
		 SELECT 'N20|2026-11-12|' || i || '|0|0', 'N20', now(), 0, 0, 'n', 1, false, false, 'bulk-' || (i / 3), now()
		 FROM generate_series(0, 299999) AS i`,
		// One more fire of 2, one single detection, and one of each excluded kind.
		`INSERT INTO satellite_detections (detection_key, satellite, detected_at, latitude, longitude,
		   confidence, frp_mw, late, retracted, fire_ref, received_at) VALUES
		 ('a1', 'N21', now(), 0, 0, 'h', 1, false, false, 'fire-a', now()),
		 ('a2', 'N20', now(), 0, 0, 'n', 1, false, false, 'fire-a', now()),
		 ('b1', 'N20', now(), 0, 0, 'n', 1, false, false, 'fire-b', now()),
		 ('late', 'N20', now() - interval '1 day', 0, 0, 'n', 1, true, false, NULL, now()),
		 ('low', 'N20', now(), 0, 0, 'l', 1, false, false, NULL, now()),
		 ('gone', 'N20', now(), 0, 0, 'n', 1, false, true, NULL, now())`,
		`ANALYZE satellite_detections`,
	}
	for _, statement := range fixtures {
		if err := ctrl.DB.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	var schema string
	if err := ctrl.DB.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	slow := strictController(t, schema, "5")
	// The short session timeout really does stop a plain scan of the table.
	if err := slow.DB.Exec("SELECT count(*) FROM (SELECT fire_ref FROM satellite_detections GROUP BY fire_ref) g").Error; err == nil {
		t.Fatal("fixture too small: a full scan finished inside the 5 ms session timeout")
	}

	stats, err := slow.SatelliteCounts(ctx)
	if err != nil {
		t.Fatalf("stats must run under their own timeout, not the session's: %v", err)
	}
	if stats.Fires != 100001 || stats.SingleDetections != 1 || stats.LateDetections != 1 ||
		stats.LowConfidence != 1 || stats.Retracted != 1 || stats.NewestDetection == nil {
		t.Fatalf("unexpected counts %+v", stats)
	}
}

// strictController opens the test schema with a session statement_timeout (ms),
// like the cluster's Patroni setting.
func strictController(t *testing.T, schema, timeoutMS string) *controller.Controller {
	t.Helper()
	config, err := pgxpool.ParseConfig(os.Getenv("AAT_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["statement_timeout"] = timeoutMS
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return &controller.Controller{DB: db}
}
