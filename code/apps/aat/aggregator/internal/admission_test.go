package internal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
	"aat/internal/httpkit"
	"github.com/gin-gonic/gin"
)

func TestReadBucketRefillsAndBoundsBurst(t *testing.T) {
	now := time.Now()
	bucket := &readBucket{rate: 4, burst: 2, tokens: 2, last: now}
	if !bucket.allow(now) || !bucket.allow(now) || bucket.allow(now) {
		t.Fatal("must admit exactly the initial burst")
	}
	if bucket.allow(now.Add(249*time.Millisecond)) || !bucket.allow(now.Add(250*time.Millisecond)) {
		t.Fatal("one new token should become available after 250ms")
	}
	if bucket.allow(now) {
		t.Fatal("an older timestamp must not refill tokens")
	}
	// Long idle periods must not accumulate more than the configured burst.
	later := now.Add(time.Hour)
	if !bucket.allow(later) || !bucket.allow(later) || bucket.allow(later) {
		t.Fatal("idle refill exceeded burst")
	}
}

func TestReadBucketConcurrentCallersShareCapacity(t *testing.T) {
	now := time.Now()
	bucket := &readBucket{rate: 80, burst: 20, tokens: 20, last: now}
	var admitted atomic.Int64
	var workers sync.WaitGroup
	for range 100 {
		workers.Go(func() {
			if bucket.allow(now) {
				admitted.Add(1)
			}
		})
	}
	workers.Wait()
	if admitted.Load() != 20 {
		t.Fatal("concurrent requests exceeded burst:", admitted.Load())
	}
}

func TestReadRateSharedAcrossRoutesAndIndependentAcrossReplicas(t *testing.T) {
	newReplica := func() http.Handler {
		return NewHandlerWithAudit(&controller.Controller{ReadRPSLimit: 1, ReadRPSBurst: 2}, "test", "audit")
	}
	request := func(handler http.Handler, method, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{"))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	first, second := newReplica(), newReplica()
	if w := request(first, "GET", "/internal/hazards?limit=0", "wrong"); w.Code != 401 {
		t.Fatalf("authentication must run before admission: %d", w.Code)
	}
	// Invalid queries exercise the real route wiring without database access.
	for _, path := range []string{"/internal/hazards?limit=0", "/internal/analytics?from=invalid"} {
		if w := request(first, "GET", path, "test"); w.Code != 400 || w.Header().Get("X-Read-RPS-Limit") != "1" {
			t.Fatalf("request did not use the shared read bucket: %d %s", w.Code, w.Body)
		}
	}
	if w := request(first, "GET", "/internal/hazards?limit=0", "test"); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("expected controlled rejection: %d %v", w.Code, w.Header())
	}
	if w := request(second, "GET", "/internal/hazards?limit=0", "test"); w.Code != 400 {
		t.Fatalf("one replica exhausted another's tokens: %d", w.Code)
	}
	for _, tc := range []struct{ method, path, token string }{
		{"POST", "/internal/ingest/bmkg", "test"},
		{"GET", "/internal/audit/events?limit=0", "audit"},
	} {
		if w := request(first, tc.method, tc.path, tc.token); w.Code != 400 || w.Header().Get("X-Read-RPS-Limit") != "" {
			t.Fatalf("read rate limit leaked into %s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}

func TestReadRateDisabled(t *testing.T) {
	router := httpkit.Router()
	router.GET("/read", limitReadRate(0, 0), func(c *gin.Context) { c.Status(204) })
	for range 100 {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/read", nil))
		if w.Code != 204 {
			t.Fatal("disabled limiter rejected request:", w.Code)
		}
	}
}

func TestReadRateAlsoLimitsCacheHits(t *testing.T) {
	ctrl := &controller.Controller{Store: &cachedHazardsStore{revision: 1}, ReadRPSLimit: 1, ReadRPSBurst: 2}
	handler := NewHandler(ctrl, "test-only")
	for _, cache := range []string{"miss", "hit"} {
		w := hazardRequest(handler, "order=recent&limit=50")
		if w.Code != 200 || w.Header().Get("X-Hazard-Cache") != cache {
			t.Fatalf("expected admitted cache %s: %d %s", cache, w.Code, w.Body)
		}
	}
	if w := hazardRequest(handler, "order=recent&limit=50"); w.Code != 429 {
		t.Fatalf("cache bypassed rate limit: %d", w.Code)
	}
}

func TestReadAdmissionReservesCapacityAndReleasesSlot(t *testing.T) {
	router := httpkit.Router()
	gate := limitReads(1)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	private := router.Group("", httpkit.Auth("Authorization", "Bearer test"))
	private.GET("/hold", gate, func(c *gin.Context) {
		close(entered)
		<-release
		c.Status(http.StatusNoContent)
	})
	private.GET("/read", gate, func(c *gin.Context) { c.Status(http.StatusBadRequest) })
	router.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	request := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	go func() { request("/hold", "Bearer test"); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	if w := request("/read", "Bearer test"); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("busy read: %d %v", w.Code, w.Header())
	}
	if w := request("/read", "Bearer wrong"); w.Code != 401 {
		t.Fatalf("authentication bypassed: %d", w.Code)
	}
	if w := request("/health", ""); w.Code != 200 {
		t.Fatalf("health blocked: %d", w.Code)
	}
	release <- struct{}{}
	<-done
	for range 2 {
		if w := request("/read", "Bearer test"); w.Code != 400 {
			t.Fatalf("slot leaked after handler error: %d", w.Code)
		}
	}
}
