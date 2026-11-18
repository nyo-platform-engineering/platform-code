package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"aat/aggregator/internal/model"
	"gorm.io/gorm"
)

// Runs against a real Citus formation, never a mock sharding implementation.
func TestCitusDistributionAndTransactions(t *testing.T) {
	if os.Getenv("AAT_TEST_CITUS") != "1" {
		t.Skip("set AAT_TEST_CITUS=1 with a three-worker Citus formation")
	}
	db := testPostgres(t)
	ctx := context.Background()
	// Upgrade the original M1 primary key without changing stored identities.
	if err := db.Exec("ALTER TABLE hazard_events DROP CONSTRAINT hazard_events_pkey, ADD PRIMARY KEY (id)").Error; err != nil {
		t.Fatal(err)
	}
	if err := model.Migrate(ctx, db); err != nil {
		t.Fatal("legacy primary key migration:", err)
	}
	// Exercise conversion of an existing populated table as well as fresh writes.
	when := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	event := model.HazardEvent{ID: "before-distribution", Source: "BMKG", Ref: "legacy", Type: "SEISMIC", Severity: "NORMAL", Area: "Jawa", Occurred: when, Ingested: when, Attributes: model.Record{}}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := EnableCitus(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnableCitus(ctx, db); err != nil {
		t.Fatal("repeat startup:", err)
	}
	if err := model.Migrate(ctx, db); err != nil {
		t.Fatal("repeat schema migration:", err)
	}
	events := []model.HazardEvent{}
	for i := 0; i < 120; i++ {
		h := event
		h.ID = fmt.Sprintf("hz-%03d", i)
		h.Ref = fmt.Sprintf("same-region-%03d", i)
		h.Ingested = when.Add(time.Duration(i) * time.Microsecond)
		events = append(events, h)
	}
	save := func(tx *gorm.DB) error { _, err := NewPostgres(tx, Identity{}).Save(ctx, events); return err }
	if err := db.Transaction(save); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(save); err != nil {
		t.Fatal("dedup:", err)
	}
	store := NewPostgres(db, Identity{})
	sum, err := store.Checksum(ctx, "")
	if err != nil || sum.Partial || sum.Count != 121 {
		t.Fatalf("checksum: %+v %v", sum, err)
	}
	var outboxes int64
	if err := db.Model(&model.HazardOutbox{}).Count(&outboxes).Error; err != nil || outboxes != 120 {
		t.Fatalf("outboxes: %d %v", outboxes, err)
	}
	var groups int64
	if err := db.Raw(`SELECT count(DISTINCT p.groupid) FROM pg_dist_shard s JOIN pg_dist_placement p USING(shardid) WHERE s.logicalrelid='hazard_events'::regclass`).Scan(&groups).Error; err != nil || groups != 3 {
		t.Fatalf("placement: %d %v", groups, err)
	}
	// Original coordinator rows must not be a hidden full copy.
	var local int64
	if err := db.Raw(`SELECT pg_relation_size('hazard_events')`).Scan(&local).Error; err != nil || local != 0 {
		t.Fatalf("coordinator copy: %d %v", local, err)
	}
	seen := map[string]bool{}
	q := EventQuery{Limit: 7}
	for {
		page, err := store.Events(ctx, q)
		if err != nil || page.Partial {
			t.Fatalf("page: %+v %v", page, err)
		}
		for _, h := range page.Items {
			if seen[h.ID] {
				t.Fatal("duplicate page item", h.ID)
			}
			seen[h.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		q.Cursor = *page.NextCursor
	}
	if len(seen) != 121 {
		t.Fatalf("global pagination: %d", len(seen))
	}
	// Canonical and local outbox writes must roll back together.
	rollback := fmt.Errorf("rollback")
	h := event
	h.Ref = "rollback"
	h.ID = "rollback"
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := NewPostgres(tx, Identity{}).Save(ctx, []model.HazardEvent{h}); err != nil {
			return err
		}
		return rollback
	})
	if err != rollback {
		t.Fatal(err)
	}
	sum, err = store.Checksum(ctx, "")
	if err != nil || sum.Count != 121 {
		t.Fatalf("rollback leaked: %+v %v", sum, err)
	}
}
