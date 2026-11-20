package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aat/aggregator/internal/storage"
	"github.com/gin-gonic/gin"
)

type delayedRevision struct {
	started, release chan struct{}
	calls            int
}

func (reader *delayedRevision) Revision(context.Context) (int64, error) {
	reader.calls++
	if reader.calls == 1 {
		close(reader.started)
		<-reader.release
	}
	return int64(reader.calls), nil
}

func TestRevisionBatchNeverReusesReadStartedBeforeRequest(t *testing.T) {
	cache := &HazardPageCache{}
	reader := &delayedRevision{started: make(chan struct{}), release: make(chan struct{})}
	first, second := make(chan int64, 1), make(chan int64, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { value, _ := cache.readRevision(ctx, reader); first <- value }()
	<-reader.started
	go func() { value, _ := cache.readRevision(ctx, reader); second <- value }()
	// Wait for the second caller to enqueue while the old SELECT is in flight.
	for {
		cache.revisionMu.Lock()
		queued := len(cache.pending)
		cache.revisionMu.Unlock()
		if queued > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("second request did not enqueue")
		}
		time.Sleep(time.Millisecond)
	}
	close(reader.release)
	if value := <-first; value != 1 {
		t.Fatal("first revision", value)
	}
	if value := <-second; value != 2 {
		t.Fatal("later request reused a SELECT started before it arrived", value)
	}
}

func TestCachedResponseNegotiatesGzipWithoutChangingJSON(t *testing.T) {
	cache := &HazardPageCache{}
	body := []byte(`{"items":[],"partial":false,"unavailable_units":[]}`)
	page := cache.put(storage.HazardQuery{Order: "recent", Limit: 50}, 1, body)
	for _, encoding := range []string{"gzip", "br, gzip;q=0.5", "gzip;q=0", "gzip;q=bogus", "gzip;q=NaN", ""} {
		t.Run(encoding, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/internal/hazards", nil)
			c.Request.Header.Set("Accept-Encoding", encoding)
			page.write(c)
			actual := w.Body.Bytes()
			compressed := encoding == "gzip" || encoding == "br, gzip;q=0.5"
			if (w.Header().Get("Content-Encoding") == "gzip") != compressed || w.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatalf("incorrect negotiation: %v", w.Header())
			}
			if compressed {
				reader, err := gzip.NewReader(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				actual, err = io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(actual, body) {
				t.Fatal("compression changed the canonical response")
			}
		})
	}
}

func TestHazardCacheLateFillCannotReplaceNewerRevision(t *testing.T) {
	cache := &HazardPageCache{}
	query := storage.HazardQuery{Order: "recent", Limit: 50}
	var writers sync.WaitGroup
	for i := 0; i < 64; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			if i%2 == 0 {
				cache.put(query, 1, []byte("old"))
			} else {
				cache.put(query, 2, []byte("new"))
			}
			cache.get(query, 2)
		}(i)
	}
	writers.Wait()
	if cached := cache.get(query, 2); cached == nil || string(cached.body) != "new" || cache.get(query, 1) != nil {
		t.Fatal("an older in-flight read replaced a committed newer revision")
	}
}

func TestHazardCacheMemoryIsBounded(t *testing.T) {
	cache := &HazardPageCache{}
	query := storage.HazardQuery{Order: "recent", Limit: 50}
	cache.put(query, 1, bytes.Repeat([]byte("x"), (1<<20)+1))
	if cache.get(query, 1) != nil {
		t.Fatal("oversized response retained")
	}
	for i := 1; i <= 100; i++ {
		query.Limit = i
		cache.put(query, 1, bytes.Repeat([]byte("x"), 256<<10))
		if len(cache.pages) > 32 || cache.bytes > 4<<20 {
			t.Fatal("cache grew beyond its memory budget")
		}
	}
}

// Model two completed fills invalidated by concurrent writes, followed by a
// third fill still using the sole database read slot.
type repeatedlyInvalidatedPage struct {
	storage.Reader
	cache *HazardPageCache
	query storage.HazardQuery
	calls atomic.Int64
	third chan struct{}
}

func (reader *repeatedlyInvalidatedPage) Revision(context.Context) (int64, error) {
	call := reader.calls.Add(1)
	if call <= 3 {
		done := make(chan struct{})
		if call < 3 {
			close(done)
		}
		reader.cache.mu.Lock()
		if reader.cache.fills == nil {
			reader.cache.fills = make(map[storage.HazardQuery]chan struct{})
		}
		reader.cache.fills[reader.query] = done
		reader.cache.mu.Unlock()
		if call == 3 {
			close(reader.third)
		}
	}
	return min(call, 3), nil
}

func TestCacheFollowerKeepsCoalescingAfterRepeatedInvalidation(t *testing.T) {
	cache := &HazardPageCache{}
	query := storage.HazardQuery{Order: "recent", Limit: 50}
	reader := &repeatedlyInvalidatedPage{cache: cache, query: query, third: make(chan struct{})}
	ctrl := &Controller{Store: reader, PageCache: cache}
	router := gin.New()
	router.GET("/internal/hazards", ctrl.CachedHazards, func(c *gin.Context) {
		c.JSON(429, gin.H{"error": "shared database read slot occupied by current fill"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := httptest.NewRequest("GET", "/internal/hazards?order=recent&limit=50", nil).WithContext(ctx)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); router.ServeHTTP(w, request); response <- w }()
	select {
	case <-reader.third:
	case w := <-response:
		t.Fatalf("reader abandoned coalescing before the third fill: %d %s", w.Code, w.Body)
	case <-ctx.Done():
		t.Fatal("reader never joined the next fill")
	}
	cache.put(query, 3, []byte(`{"items":[],"partial":false,"unavailable_units":[]}`))
	cache.endFill(query)
	select {
	case w := <-response:
		if w.Code != 200 || w.Header().Get("X-Hazard-Cache") != "hit" {
			t.Fatalf("reader did not use the latest complete fill: %d %s", w.Code, w.Body)
		}
	case <-ctx.Done():
		t.Fatal("reader did not finish after fill completed")
	}
}

func TestCacheFollowerCancellationAfterRepeatedInvalidation(t *testing.T) {
	cache := &HazardPageCache{}
	query := storage.HazardQuery{Order: "recent", Limit: 50}
	reader := &repeatedlyInvalidatedPage{cache: cache, query: query, third: make(chan struct{})}
	ctrl := &Controller{Store: reader, PageCache: cache}
	router := gin.New()
	router.GET("/internal/hazards", ctrl.CachedHazards, func(c *gin.Context) {
		if c.Request.Context().Err() == nil {
			c.JSON(429, gin.H{"error": "unexpected fallback while fill active"})
			return
		}
		c.JSON(503, gin.H{"error": "request canceled"})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("GET", "/internal/hazards?order=recent&limit=50", nil).WithContext(ctx)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); router.ServeHTTP(w, request); response <- w }()
	select {
	case <-reader.third:
	case w := <-response:
		t.Fatalf("reader abandoned fill before cancellation: %d", w.Code)
	case <-time.After(time.Second):
		t.Fatal("reader did not join third fill")
	}
	cancel()
	select {
	case w := <-response:
		if w.Code != 503 {
			t.Fatalf("unexpected cancellation response: %d", w.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("cache follower ignored request cancellation")
	}
}
