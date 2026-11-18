package worker

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

type effectEntry struct {
	jetstream.KeyValueEntry
	value []byte
}

func (e effectEntry) Value() []byte { return e.value }

type memoryEffects struct {
	mu           sync.Mutex
	values       map[string][]byte
	writeErr     error
	commitOnErr  bool
	readErr      error
	readAfterErr error
}

func (s *memoryEffects) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.readErr != nil {
		return nil, s.readErr
	}
	value, ok := s.values[key]
	if !ok {
		return nil, jetstream.ErrKeyNotFound
	}
	return effectEntry{value: append([]byte(nil), value...)}, nil
}

func (s *memoryEffects) Create(ctx context.Context, key string, value []byte, _ ...jetstream.KVCreateOpt) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, ok := s.values[key]; ok {
		return 0, jetstream.ErrKeyExists
	}
	if s.writeErr == nil || s.commitOnErr {
		if s.values == nil {
			s.values = map[string][]byte{}
		}
		s.values[key] = append([]byte(nil), value...)
	}
	s.readErr = s.readAfterErr
	return 1, s.writeErr
}

func TestEffectRetryAfterUncertainCommit(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		name := "retry-after-reconnect"
		if confirm {
			name = "confirm-lost-write-response"
		}
		t.Run(name, func(t *testing.T) {
			store := &memoryEffects{writeErr: errors.New("write response lost"), commitOnErr: true}
			if !confirm {
				store.readAfterErr = errors.New("connection closed")
			}
			hazard := Hazard{ID: "haz-one", Severity: "AWAS"}
			project := func(Hazard) string { return "field_alert" }
			_, created, err := recordEffect(context.Background(), store, "delivery-1", hazard, project)
			if created || (err == nil) != confirm {
				t.Fatalf("uncertain commit: created=%v error=%v", created, err)
			}
			store.writeErr, store.readErr = nil, nil
			effect, created, err := recordEffect(context.Background(), store, "delivery-1", hazard, func(Hazard) string {
				t.Fatal("retry reprojected an already committed effect")
				return ""
			})
			if err != nil || created || effect.Kind != "field_alert" || len(store.values) != 1 {
				t.Fatalf("retry changed committed effect: %+v created=%v error=%v", effect, created, err)
			}
		})
	}
}

func TestEffectFailureCanRetryWithoutLosingUpdate(t *testing.T) {
	ctx := context.Background()
	store := &memoryEffects{writeErr: errors.New("broker unavailable")}
	hazard := Hazard{ID: "haz-one", Severity: "SIAGA"}
	project := func(Hazard) string { return "field_alert" }
	if _, _, err := recordEffect(ctx, store, "version-1", hazard, project); err == nil || len(store.values) != 0 {
		t.Fatal("failed write was treated as committed")
	}
	store.writeErr = nil
	if _, created, err := recordEffect(ctx, store, "version-1", hazard, project); err != nil || !created {
		t.Fatalf("retry: created=%v error=%v", created, err)
	}
	hazard.Severity = "AWAS"
	if _, _, err := recordEffect(ctx, store, "version-1", hazard, project); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("same delivery ID silently changed payload: %v", err)
	}
	if _, created, err := recordEffect(ctx, store, "version-2", hazard, project); err != nil || !created || len(store.values) != 2 {
		t.Fatalf("new version lost: created=%v error=%v", created, err)
	}
}

func TestConcurrentEffectCreatesOneRecord(t *testing.T) {
	store := &memoryEffects{}
	var wg sync.WaitGroup
	created := make(chan bool, 24)
	for range cap(created) {
		wg.Go(func() {
			_, fresh, err := recordEffect(context.Background(), store, "same-delivery", Hazard{ID: "haz-one"}, func(Hazard) string { return "dashboard_update" })
			if err != nil {
				t.Error(err)
			}
			created <- fresh
		})
	}
	wg.Wait()
	close(created)
	count := 0
	for fresh := range created {
		if fresh {
			count++
		}
	}
	if count != 1 || len(store.values) != 1 {
		t.Fatalf("created=%d records=%d", count, len(store.values))
	}
}

func TestEffectReadFailureDoesNotWrite(t *testing.T) {
	store := &memoryEffects{readErr: errors.New("broker unavailable")}
	if _, _, err := recordEffect(context.Background(), store, "delivery", Hazard{ID: "haz-one"}, func(Hazard) string {
		t.Fatal("projection ran without confirming existing state")
		return ""
	}); err == nil || len(store.values) != 0 {
		t.Fatal("read failure was ignored")
	}
}
