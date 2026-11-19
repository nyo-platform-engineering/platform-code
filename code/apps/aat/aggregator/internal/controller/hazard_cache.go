package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"aat/aggregator/internal/storage"
	"github.com/gin-gonic/gin"
)

type cachedHazardPage struct {
	body, compressed []byte
}

func (page *cachedHazardPage) write(c *gin.Context) {
	c.Header("Vary", "Accept-Encoding")
	if acceptsGzip(c.GetHeader("Accept-Encoding")) {
		c.Header("Content-Encoding", "gzip")
		c.Data(200, "application/json; charset=utf-8", page.compressed)
		return
	}
	c.Data(200, "application/json; charset=utf-8", page.body)
}

func acceptsGzip(header string) bool {
	for _, encoding := range strings.Split(header, ",") {
		parts := strings.Split(encoding, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || parsed <= 0 || parsed > 1 {
					return false
				}
				quality = parsed
			}
		}
		return quality > 0
	}
	return false
}

// HazardPageCache retains only encoded first pages, never the whole dataset.
// Callers must read the committed storage revision before every lookup. There
// is no TTL or stale fallback: a changed/unreadable revision requires a new read.
type HazardPageCache struct {
	mu       sync.RWMutex
	revision int64
	pages    map[storage.HazardQuery]*cachedHazardPage
	bytes    int
	fills    map[storage.HazardQuery]chan struct{}

	revisionMu sync.Mutex
	pending    []revisionRequest
	reading    bool
}

func (cache *HazardPageCache) beginFill(query storage.HazardQuery) (<-chan struct{}, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if done := cache.fills[query]; done != nil {
		return done, false
	}
	if cache.fills == nil {
		cache.fills = make(map[storage.HazardQuery]chan struct{})
	}
	done := make(chan struct{})
	cache.fills[query] = done
	return done, true
}

func (cache *HazardPageCache) endFill(query storage.HazardQuery) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	close(cache.fills[query])
	delete(cache.fills, query)
}

type revisionResult struct {
	value int64
	err   error
}

type revisionRequest struct {
	ctx    context.Context
	result chan revisionResult
}

// Validate queued requests together, with the SELECT starting after every member
// of that batch arrived. Requests arriving during that SELECT join the NEXT
// batch. This reduces round trips without reusing an older revision or a TTL.
func (cache *HazardPageCache) readRevision(ctx context.Context, reader storage.RevisionReader) (int64, error) {
	request := revisionRequest{ctx: ctx, result: make(chan revisionResult, 1)}
	cache.revisionMu.Lock()
	cache.pending = append(cache.pending, request)
	if !cache.reading {
		cache.reading = true
		go cache.readRevisions(reader)
	}
	cache.revisionMu.Unlock()
	select {
	case result := <-request.result:
		return result.value, result.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (cache *HazardPageCache) readRevisions(reader storage.RevisionReader) {
	for {
		cache.revisionMu.Lock()
		batch := cache.pending
		cache.pending = nil
		if len(batch) == 0 {
			cache.reading = false
			cache.revisionMu.Unlock()
			return
		}
		cache.revisionMu.Unlock()
		// One caller disconnecting must not cancel the other batch members.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(batch[0].ctx), time.Second)
		value, err := reader.Revision(ctx)
		cancel()
		for _, request := range batch {
			request.result <- revisionResult{value: value, err: err}
		}
	}
}

func (cache *HazardPageCache) get(query storage.HazardQuery, revision int64) *cachedHazardPage {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if cache.revision != revision {
		return nil
	}
	return cache.pages[query]
}

func (cache *HazardPageCache) put(query storage.HazardQuery, revision int64, body []byte) *cachedHazardPage {
	if len(body) > 1<<20 {
		return nil
	}
	// Compress once on fill, so repeated reads avoid JSON and gzip CPU work.
	var compressed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	_, _ = writer.Write(body)
	_ = writer.Close()
	page := &cachedHazardPage{body: body, compressed: compressed.Bytes()}
	size := len(page.body) + len(page.compressed)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if revision < cache.revision {
		return page
	}
	if revision != cache.revision || len(cache.pages) >= 32 || cache.bytes+size > 4<<20 {
		cache.pages = nil
		cache.bytes = 0
		cache.revision = revision
	}
	if cache.pages == nil {
		cache.pages = make(map[storage.HazardQuery]*cachedHazardPage)
	}
	if previous := cache.pages[query]; previous != nil {
		cache.bytes -= len(previous.body) + len(previous.compressed)
	}
	cache.pages[query] = page
	cache.bytes += size
	return page
}
