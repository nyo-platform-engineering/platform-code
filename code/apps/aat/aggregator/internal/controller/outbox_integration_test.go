package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"aat/aggregator/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func outboxTestController(t *testing.T) *Controller {
	t.Helper()
	url := os.Getenv("AAT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AAT_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("aat_outbox_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(ctx)
	})
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	sqlDB := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.HazardOutbox{}); err != nil {
		t.Fatal(err)
	}
	return &Controller{DB: db}
}

func TestOutboxClaimFencesStalePublisher(t *testing.T) {
	ctrl := outboxTestController(t)
	ctx := context.Background()
	row := model.HazardOutbox{EventKey: "hazard-version-1", Payload: json.RawMessage(`{"hazard_id":"haz-test"}`)}
	if err := ctrl.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	claim := func() model.OutboxMessage {
		t.Helper()
		items, err := ctrl.ClaimOutbox(ctx, 1, time.Minute)
		if err != nil || len(items) != 1 {
			t.Fatalf("claim: %+v, %v", items, err)
		}
		return items[0]
	}
	first := claim()
	if items, err := ctrl.ClaimOutbox(ctx, 1, time.Minute); err != nil || len(items) != 0 {
		t.Fatalf("active claim taken twice: %+v, %v", items, err)
	}
	if err := ctrl.MarkOutboxPublished(ctx, first.ID, first.Attempts+1); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("unowned claim acknowledged: %v", err)
	}
	if err := ctrl.DB.Model(&model.HazardOutbox{}).Where("id = ?", row.ID).
		Update("lease_until", gorm.Expr("now() - interval '1 second'")).Error; err != nil {
		t.Fatal(err)
	}
	if err := ctrl.MarkOutboxPublished(ctx, first.ID, first.Attempts); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("expired claim acknowledged before takeover: %v", err)
	}
	second := claim()
	if second.ID != first.ID || second.Attempts != first.Attempts+1 || second.EventKey != first.EventKey || !bytes.Equal(second.Payload, first.Payload) {
		t.Fatalf("retry changed message identity or payload: first=%+v second=%+v", first, second)
	}
	if err := ctrl.MarkOutboxPublished(ctx, first.ID, first.Attempts); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("stale publisher acknowledged replacement claim: %v", err)
	}
	var pending model.HazardOutbox
	if err := ctrl.DB.First(&pending, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if pending.PublishedAt != nil || pending.LeaseUntil == nil || !bytes.Contains(pending.Payload, []byte("haz-test")) {
		t.Fatalf("stale acknowledgement changed pending message: %+v", pending)
	}
	if err := ctrl.MarkOutboxPublished(ctx, second.ID, second.Attempts); err != nil {
		t.Fatal(err)
	}
	var published model.HazardOutbox
	if err := ctrl.DB.First(&published, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if published.PublishedAt == nil || published.LeaseUntil != nil || string(published.Payload) != "{}" {
		t.Fatalf("current owner did not finish cleanup: %+v", published)
	}
	if err := ctrl.MarkOutboxPublished(ctx, second.ID, second.Attempts); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("finished claim acknowledged again: %v", err)
	}
	if items, err := ctrl.ClaimOutbox(ctx, 1, time.Minute); err != nil || len(items) != 0 {
		t.Fatalf("published message reclaimed: %+v, %v", items, err)
	}
}

func TestOutboxConcurrentPublishersClaimDistinctMessages(t *testing.T) {
	ctrl := outboxTestController(t)
	ctx := context.Background()
	const publishers = 12
	for i := range publishers {
		row := model.HazardOutbox{EventKey: fmt.Sprintf("event-%d", i), Payload: json.RawMessage(`{"hazard_id":"haz-test"}`)}
		if err := ctrl.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan model.OutboxMessage, publishers)
	var wg sync.WaitGroup
	for range publishers {
		wg.Go(func() {
			items, err := ctrl.ClaimOutbox(ctx, 1, time.Minute)
			if err != nil || len(items) != 1 {
				t.Errorf("concurrent claim: %+v, %v", items, err)
				return
			}
			results <- items[0]
		})
	}
	wg.Wait()
	close(results)
	seen := map[int64]bool{}
	for item := range results {
		if seen[item.ID] || item.Attempts != 1 {
			t.Errorf("message claimed more than once: %+v", item)
		}
		seen[item.ID] = true
		if err := ctrl.MarkOutboxPublished(ctx, item.ID, item.Attempts); err != nil {
			t.Error(err)
		}
	}
	if len(seen) != publishers {
		t.Fatalf("claimed %d distinct messages, want %d", len(seen), publishers)
	}
}
