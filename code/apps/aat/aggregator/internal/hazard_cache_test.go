package internal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
)

type cachedHazardsStore struct {
	auditStore
	revision                   int64
	reads                      int
	partial, offline, changing bool
}

func (s *cachedHazardsStore) Revision(context.Context) (int64, error) {
	if s.offline {
		return 0, errors.New("coordinator unavailable")
	}
	return s.revision, nil
}

func (s *cachedHazardsStore) Page(context.Context, storage.HazardQuery) (storage.HazardPage, error) {
	s.reads++
	if s.changing {
		s.revision++
	}
	page := storage.HazardPage{Items: []model.HazardEvent{{ID: fmt.Sprintf("haz-%d", s.revision)}},
		Availability: storage.Availability{UnavailableUnits: []string{}}}
	if s.partial {
		page.Partial = true
		page.Items = []model.HazardEvent{}
		page.UnavailableUnits = []string{"missing-worker"}
	}
	return page, nil
}

func hazardRequest(handler http.Handler, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/internal/hazards?"+query, nil)
	r.Header.Set("Authorization", "Bearer test-only")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestHazardCacheInvalidatesAcrossReplicas(t *testing.T) {
	store := &cachedHazardsStore{revision: 1}
	first := NewHandler(&controller.Controller{Store: store}, "test-only")
	second := NewHandler(&controller.Controller{Store: store}, "test-only")
	for _, handler := range []http.Handler{first, second} {
		if w := hazardRequest(handler, "order=recent&limit=50"); w.Header().Get("X-Hazard-Cache") != "miss" {
			t.Fatalf("first read: %d %s", w.Code, w.Body)
		}
		if w := hazardRequest(handler, "order=recent&limit=50"); w.Header().Get("X-Hazard-Cache") != "hit" {
			t.Fatalf("repeated read: %d %s", w.Code, w.Body)
		}
	}
	if store.reads != 2 {
		t.Fatal("each replica should fill once:", store.reads)
	}
	store.revision++ // A transaction committed through another instance.
	for _, handler := range []http.Handler{first, second} {
		w := hazardRequest(handler, "order=recent&limit=50")
		if w.Header().Get("X-Hazard-Cache") != "miss" || !strings.Contains(w.Body.String(), "haz-2") {
			t.Fatalf("stale data after commit: %s", w.Body)
		}
	}
}

func TestHazardCacheDoesNotHideUnavailableStorage(t *testing.T) {
	store := &cachedHazardsStore{revision: 1}
	handler := NewHandler(&controller.Controller{Store: store}, "test-only")
	hazardRequest(handler, "order=recent&limit=50")
	store.offline, store.partial = true, true
	for i := 0; i < 2; i++ {
		w := hazardRequest(handler, "order=recent&limit=50")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"partial":true`) || w.Header().Get("X-Hazard-Cache") == "hit" {
			t.Fatalf("cached success hid unavailable storage: %d %s", w.Code, w.Body)
		}
	}
	if store.reads != 3 {
		t.Fatal("partial responses must not be cached:", store.reads)
	}
}

func TestHazardCacheRejectsFillWhenRevisionChangesDuringRead(t *testing.T) {
	store := &cachedHazardsStore{revision: 1, changing: true}
	handler := NewHandler(&controller.Controller{Store: store}, "test-only")
	for i := 0; i < 2; i++ {
		w := hazardRequest(handler, "order=recent&limit=50")
		if w.Code != 200 || w.Header().Get("X-Hazard-Cache") != "" {
			t.Fatalf("cached a racing snapshot: %d %s", w.Code, w.Body)
		}
	}
	if store.reads != 2 {
		t.Fatal("must read again after a racing commit")
	}
}

func TestHazardCacheSeparatesFiltersAndBypassesPagination(t *testing.T) {
	store := &cachedHazardsStore{revision: 1}
	handler := NewHandler(&controller.Controller{Store: store}, "test-only")
	for _, query := range []string{"order=recent&limit=50", "order=recent&limit=25", "order=recent&limit=50&source=BMKG"} {
		if w := hazardRequest(handler, query); w.Header().Get("X-Hazard-Cache") != "miss" {
			t.Fatal("distinct query reused cached page", query)
		}
	}
	for _, query := range []string{"order=id&limit=50", "order=recent&limit=101", "order=recent&limit=50&cursor=retry"} {
		if w := hazardRequest(handler, query); w.Header().Get("X-Hazard-Cache") != "" {
			t.Fatal("cached a non-first/small recent page", query)
		}
	}
	if store.reads != 6 {
		t.Fatal("queries bypassed storage unexpectedly:", store.reads)
	}
}

type busyHazardStore struct {
	cachedHazardsStore
	entered, release chan struct{}
}

func (s *busyHazardStore) Page(ctx context.Context, q storage.HazardQuery) (storage.HazardPage, error) {
	if q.Order == "id" {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return storage.HazardPage{}, ctx.Err()
		}
	}
	return s.cachedHazardsStore.Page(ctx, q)
}

func TestCachedReadsDoNotConsumeDistributedQuerySlots(t *testing.T) {
	store := &busyHazardStore{cachedHazardsStore: cachedHazardsStore{revision: 1},
		entered: make(chan struct{}), release: make(chan struct{})}
	handler := NewHandler(&controller.Controller{Store: store, ReadConcurrency: 1}, "test-only")
	hazardRequest(handler, "order=recent&limit=50")
	done := make(chan struct{})
	go func() {
		defer close(done)
		hazardRequest(handler, "order=id&limit=50")
	}()
	defer func() { close(store.release); <-done }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("uncached query did not start")
	}
	if w := hazardRequest(handler, "order=recent&limit=50"); w.Code != 200 || w.Header().Get("X-Hazard-Cache") != "hit" {
		t.Fatalf("warm cache was blocked by distributed SQL: %d %s", w.Code, w.Body)
	}
	if w := hazardRequest(handler, "order=id&limit=50"); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("uncached reads bypassed the database limit: %d %s", w.Code, w.Body)
	}
}

type coldHazardStore struct {
	cachedHazardsStore
	started, release chan struct{}
	once             sync.Once
	calls            atomic.Int64
}

func (s *coldHazardStore) Page(ctx context.Context, q storage.HazardQuery) (storage.HazardPage, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return storage.HazardPage{Items: []model.HazardEvent{{ID: "filled"}},
			Availability: storage.Availability{UnavailableUnits: []string{}}}, nil
	case <-ctx.Done():
		return storage.HazardPage{}, ctx.Err()
	}
}

func TestConcurrentColdReadsShareOneFill(t *testing.T) {
	store := &coldHazardStore{cachedHazardsStore: cachedHazardsStore{revision: 1},
		started: make(chan struct{}), release: make(chan struct{})}
	handler := NewHandler(&controller.Controller{Store: store, ReadConcurrency: 1}, "test-only")
	responses := make(chan *httptest.ResponseRecorder, 20)
	for i := 0; i < cap(responses); i++ {
		go func() { responses <- hazardRequest(handler, "order=recent&limit=50") }()
	}
	<-store.started
	close(store.release)
	for i := 0; i < cap(responses); i++ {
		w := <-responses
		if w.Code != 200 || !strings.Contains(w.Body.String(), "filled") {
			t.Fatalf("cold read rejected instead of sharing fill: %d %s", w.Code, w.Body)
		}
	}
	if store.calls.Load() != 1 {
		t.Fatal("identical cold requests stampeded storage:", store.calls.Load())
	}
}
