package aggregate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Runs the same storage contract against a fresh schema and an upgraded legacy table.
// Requires AAT_TEST_DATABASE_URL; each case creates and cleans up its own schema.
// Steps stay sequential because migration, ingestion, and updates share the stored rows.
func TestStoreSchemaAndIngestion(t *testing.T) {
	url := os.Getenv("AAT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AAT_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	for _, tc := range []struct {
		name   string
		legacy bool
	}{
		{"fresh_database", false},
		{"upgrade_legacy_jsonb_table", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := tc.legacy
			// Arrange an isolated schema; never modify the application's tables.
			schemaName := fmt.Sprintf("aat_test_%d", time.Now().UnixNano())
			if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := admin.Exec(ctx, "DROP SCHEMA "+schemaName+" CASCADE"); err != nil {
					t.Error(err)
				}
			}()
			config, err := pgxpool.ParseConfig(url)
			if err != nil {
				t.Fatal(err)
			}
			config.ConnConfig.RuntimeParams["search_path"] = schemaName
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			store := Store{Pool: pool}
			original, err := Volcanic(report("v1"))
			if err != nil {
				t.Fatal(err)
			}
			// PostgreSQL timestamps have microsecond precision.
			original.Ingested = original.Ingested.Truncate(time.Microsecond)
			// Only the upgrade case starts with an existing JSONB document.
			if legacy {
				_, err = pool.Exec(ctx, "CREATE TABLE hazards(id text PRIMARY KEY, source text NOT NULL, document jsonb NOT NULL)")
				if err != nil {
					t.Fatal(err)
				}
				document, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, "INSERT INTO hazards VALUES($1,$2,$3)", original.ID, original.Source, document); err != nil {
					t.Fatal(err)
				}
			}
			// Repeated initialization must be safe for service restarts.
			for range 2 {
				if err := store.Init(ctx); err != nil {
					t.Fatal(err)
				}
			}
			// A migrated event is unchanged; a fresh event is inserted once.
			changed, err := store.Ingest(ctx, "PVMBG", map[string][]Record{"volcanic_reports": {report("v1")}})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if legacy {
				want = 0
			}
			if len(changed) != want {
				t.Fatalf("first ingest changed=%d, want %d", len(changed), want)
			}
			before, err := store.List(ctx, "PVMBG", "", 100)
			if err != nil || len(before) != 1 {
				t.Fatalf("list: %v, %v", before, err)
			}
			if legacy && !before[0].Ingested.Equal(original.Ingested.Truncate(time.Microsecond)) {
				t.Fatal("migration changed ingestion timestamp")
			}
			// Identical ingestion must preserve ingested_at and report no change.
			changed, err = store.Ingest(ctx, "PVMBG", map[string][]Record{"volcanic_reports": {report("v1")}})
			if err != nil || len(changed) != 0 {
				t.Fatalf("repeat ingest: %v, %v", changed, err)
			}
			after, err := store.List(ctx, "PVMBG", "", 100)
			if err != nil || !before[0].Ingested.Equal(after[0].Ingested) {
				t.Fatal("repeat ingest changed timestamp", err)
			}

			// Old/new attributes coexist without another schema migration.
			dynamic := report("v2")
			dynamic["confidence_level"] = json.RawMessage(`0.85`)
			dynamic["future_sensor"] = json.RawMessage(`{"nested":[1,true]}`)
			changed, err = store.Ingest(ctx, "PVMBG", map[string][]Record{"volcanic_reports": {dynamic}})
			if err != nil || len(changed) != 1 {
				t.Fatalf("dynamic ingest: %v, %v", changed, err)
			}
			items, err := store.List(ctx, "PVMBG", "", 100)
			if err != nil || len(items) != 2 {
				t.Fatalf("old/new coexistence: %v, %v", items, err)
			}
			for _, h := range items {
				if h.Ref == "v1" {
					if _, ok := h.Attributes["confidence_level"]; ok {
						t.Fatal("old event gained new attribute")
					}
				} else if string(h.Attributes["confidence_level"]) != "0.85" || h.Attributes["future_sensor"] == nil {
					t.Fatal("dynamic attributes lost")
				}
			}
			// Changing an attribute must count as an update.
			dynamic["confidence_level"] = json.RawMessage(`0.9`)
			changed, err = store.Ingest(ctx, "PVMBG", map[string][]Record{"volcanic_reports": {dynamic}})
			if err != nil || len(changed) != 1 {
				t.Fatalf("attribute update: %v, %v", changed, err)
			}
			// Database constraints must reject invalid standardized fields.
			if _, err := pool.Exec(ctx, "UPDATE hazards SET latitude=100"); err == nil {
				t.Fatal("coordinate constraint missing")
			}
			if _, err := pool.Exec(ctx, "UPDATE hazards SET severity='invalid'"); err == nil {
				t.Fatal("severity constraint missing")
			}
			// The canonical table must no longer store the full event document.
			var documentColumns int
			err = pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema=$1 AND table_name='hazards' AND column_name='document'", schemaName).Scan(&documentColumns)
			if err != nil || documentColumns != 0 {
				t.Fatal("canonical document column still exists", err)
			}

			// A warning may arrive before its source event, then be correlated later.
			changed, err = store.Ingest(ctx, "BMKG", map[string][]Record{"tsunami_warnings": {tsunami("Awas")}})
			if err != nil || len(changed) != 0 {
				t.Fatalf("early warning: %v, %v", changed, err)
			}
			changed, err = store.Ingest(ctx, "BMKG", map[string][]Record{"seismic_events": {quake("5")}})
			if err != nil || len(changed) != 1 || changed[0].Severity != "AWAS" {
				t.Fatalf("correlation: %v, %v", changed, err)
			}
		})
	}
}
