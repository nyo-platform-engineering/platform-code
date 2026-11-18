package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"aat/aggregator/internal/model"
	"aat/internal/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	url := os.Getenv("AAT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AAT_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("audit_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
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
	if err = model.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPostgresAuditContract(t *testing.T) {
	db := testPostgres(t)
	ctx := context.Background()
	store := NewPostgres(db, Identity{Node: "database-worker", Unit: "canonical"})
	when := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	for i, ref := range []string{"z", "é", "A"} {
		event := model.HazardEvent{ID: fmt.Sprintf("hz-%d", i), Ref: ref, Source: "BMKG", Type: "SEISMIC", Severity: "WASPADA", Area: "Jawa", Occurred: when, Ingested: when, Attributes: model.Record{}}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := EventQuery{Source: "BMKG", Since: when, Limit: 2}
	page, err := store.Events(ctx, q)
	if err != nil || page.Partial || len(page.Items) != 2 || page.Items[0].ID != "hz-0" || page.Items[1].ID != "hz-1" || page.NextCursor == nil {
		t.Fatalf("first page: %+v %v", page, err)
	}
	q.Cursor = *page.NextCursor
	page, err = store.Events(ctx, q)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "hz-2" || page.NextCursor != nil {
		t.Fatalf("last page: %+v %v", page, err)
	}
	q.Source = "PVMBG"
	if _, err = store.Events(ctx, q); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor filter change: %v", err)
	}
	q.Cursor = "bad cursor"
	if _, err = store.Events(ctx, q); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("malformed cursor: %v", err)
	}
	page, err = store.Events(ctx, EventQuery{Since: when.Add(time.Microsecond), Limit: 100})
	if err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("since: %+v %v", page, err)
	}
	hash := sha256.Sum256([]byte("A\nz\né"))
	for _, source := range []string{"BMKG", ""} {
		result, err := store.Checksum(ctx, source)
		if err != nil || result.Partial || result.Count != 3 || result.Checksum == nil || *result.Checksum != hex.EncodeToString(hash[:]) || result.AsOf.Location() != time.UTC {
			t.Fatalf("checksum: %+v %v", result, err)
		}
	}
	emptyHash := sha256.Sum256(nil)
	result, err := store.Checksum(ctx, "PVMBG")
	if err != nil || result.Count != 0 || result.Checksum == nil || *result.Checksum != hex.EncodeToString(emptyHash[:]) {
		t.Fatalf("empty checksum: %+v %v", result, err)
	}
	locations, err := store.Locations(ctx, "hz-1")
	if err != nil || !reflect.DeepEqual(locations.Locations, []Location{{Node: "database-worker", Unit: "canonical", Role: "primary"}}) {
		t.Fatalf("locations: %+v %v", locations, err)
	}
	if _, err = store.Locations(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err = NewPostgres(db, Identity{}).Locations(ctx, "hz-1"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("unknown node: %v", err)
	}
	// A canceled query cannot be reported as a complete empty dataset.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	page, err = store.Events(canceled, EventQuery{Limit: 100})
	if err != nil || !page.Partial || !reflect.DeepEqual(page.UnavailableUnits, []string{"canonical"}) || len(page.Items) != 0 {
		t.Fatalf("unavailable events: %+v %v", page, err)
	}
	result, err = store.Checksum(canceled, "")
	if err != nil || !result.Partial || result.Checksum != nil {
		t.Fatalf("unavailable checksum: %+v %v", result, err)
	}
}

func TestPostgresWriterTransactionAndIdempotency(t *testing.T) {
	db := testPostgres(t)
	traceParent := "00-11223344556677889900aabbccddeeff-1122334455667788-01"
	ctx := telemetry.Propagation.Extract(context.Background(), propagation.MapCarrier{"traceparent": traceParent})
	when := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	event := model.HazardEvent{ID: "hz-original", Ref: "same-source-ref", Source: "BMKG", Type: "SEISMIC", Severity: "WASPADA", Area: "Jawa", Occurred: when, Ingested: when, Attributes: model.Record{}}
	rollback := errors.New("source processing failed")
	err := db.Transaction(func(tx *gorm.DB) error {
		var writer Writer = NewPostgres(tx, Identity{})
		if _, err := writer.Save(ctx, []model.HazardEvent{event}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	for _, table := range []any{&model.HazardEvent{}, &model.HazardOutbox{}} {
		var count int64
		if err := db.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rollback: %d %v", count, err)
		}
	}
	save := func(event model.HazardEvent) []model.HazardEvent {
		t.Helper()
		var changed []model.HazardEvent
		if err := db.Transaction(func(tx *gorm.DB) error {
			var writer Writer = NewPostgres(tx, Identity{})
			var err error
			changed, err = writer.Save(ctx, []model.HazardEvent{event})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if changed := save(event); len(changed) != 1 {
		t.Fatalf("first write: %+v", changed)
	}
	event.Ingested = when.Add(time.Minute)
	if changed := save(event); len(changed) != 0 {
		t.Fatalf("unchanged event: %+v", changed)
	}
	stored, err := NewPostgres(db, Identity{}).List(ctx, "BMKG", "", 10)
	if err != nil || len(stored) != 1 || !stored[0].Ingested.Equal(when) {
		t.Fatalf("idempotency: %+v %v", stored, err)
	}
	event.ID = "hz-new-id"
	event.Severity = "AWAS"
	if changed := save(event); len(changed) != 1 || changed[0].ID != "hz-original" {
		t.Fatalf("stable identity: %+v", changed)
	}
	var persisted model.HazardOutbox
	if err := db.Order("id").First(&persisted).Error; err != nil || persisted.TraceParent != traceParent {
		t.Fatalf("durable trace context: %+v %v", persisted, err)
	}
	var count int64
	if err := db.Model(&model.HazardOutbox{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("outbox: %d %v", count, err)
	}
}

func TestRecentPagesAndUnavailableCursorRetry(t *testing.T) {
	db := testPostgres(t)
	ctx := context.Background()
	store := NewPostgres(db, Identity{Unit: "canonical"})
	at := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		h := model.HazardEvent{ID: fmt.Sprintf("hz-%d", i), Ref: fmt.Sprint(i), Source: "BMKG", Type: "SEISMIC", Severity: "NORMAL", Area: "Jawa", Occurred: at, Ingested: at, Attributes: model.Record{}}
		if err := db.Create(&h).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := HazardQuery{Order: "recent", Limit: 2}
	seen := []string{}
	for {
		page, err := store.Page(ctx, q)
		if err != nil || page.Partial {
			t.Fatalf("page: %+v %v", page, err)
		}
		for _, h := range page.Items {
			seen = append(seen, h.ID)
		}
		if page.NextCursor == nil {
			break
		}
		q.Cursor = *page.NextCursor
	}
	if !reflect.DeepEqual(seen, []string{"hz-4", "hz-3", "hz-2", "hz-1", "hz-0"}) {
		t.Fatalf("recent timestamp ties: %v", seen)
	}
	q.Source = "PVMBG"
	if _, err := store.Page(ctx, q); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("changed source: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	partial, err := store.Page(canceled, HazardQuery{Order: "recent", Limit: 2})
	if err != nil || !partial.Partial || partial.NextCursor == nil {
		t.Fatalf("missing retry: %+v %v", partial, err)
	}
	page, err := store.Page(ctx, HazardQuery{Order: "recent", Limit: 2, Cursor: *partial.NextCursor})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != "hz-4" {
		t.Fatalf("retry skipped data: %+v %v", page, err)
	}
	audit, err := store.Events(canceled, EventQuery{Limit: 2})
	if err != nil || !audit.Partial || audit.NextCursor == nil {
		t.Fatalf("audit missing retry: %+v %v", audit, err)
	}
	recovered, err := store.Events(ctx, EventQuery{Limit: 2, Cursor: *audit.NextCursor})
	if err != nil || len(recovered.Items) != 2 || recovered.Items[0].ID != "hz-0" {
		t.Fatalf("audit retry skipped data: %+v %v", recovered, err)
	}
}
