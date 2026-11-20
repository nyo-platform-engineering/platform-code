package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"aat/aggregator/internal/model"
	"gorm.io/gorm"
)

func TestReadRevisionFollowsCommittedChangesAndLegacyOutboxWrites(t *testing.T) {
	db := testPostgres(t)
	ctx := context.Background()
	store := NewPostgres(db, Identity{})
	revision := func() int64 {
		t.Helper()
		value, err := store.Revision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := revision()
	at := time.Now().UTC()
	event := model.HazardEvent{ID: "revision-test", Ref: "revision-test", Source: "BMKG", Type: "SEISMIC",
		Severity: "NORMAL", Area: "Jawa", Occurred: at, Ingested: at, Attributes: model.Record{}}
	rollback := errors.New("rollback")
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := NewPostgres(tx, Identity{}).Save(ctx, []model.HazardEvent{event}); err != nil {
			return err
		}
		if revision() != first {
			t.Fatal("uncommitted revision escaped the write transaction")
		}
		return rollback
	})
	if err != rollback || revision() != first {
		t.Fatal("rolled-back write invalidated the committed cache revision", err)
	}
	save := func() {
		t.Helper()
		if err := db.Transaction(func(tx *gorm.DB) error {
			_, err := NewPostgres(tx, Identity{}).Save(ctx, []model.HazardEvent{event})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	committed := revision()
	if committed <= first {
		t.Fatal("commit did not advance revision")
	}
	save()
	if revision() != committed {
		t.Fatal("unchanged replay advanced revision")
	}
	event.Severity = "AWAS"
	event.Ingested = event.Ingested.Add(time.Second)
	save()
	if revision() <= committed {
		t.Fatal("update did not invalidate the cached page")
	}
	committed = revision()
	// A binary deployed before the cache change still inserts the original outbox.
	if err := db.Create(&model.HazardOutbox{EventKey: "legacy-writer", CorrelationID: "test", Payload: []byte(`{}`)}).Error; err != nil {
		t.Fatal(err)
	}
	if revision() <= committed {
		t.Fatal("rolling deployment's old writer bypassed invalidation")
	}
}
