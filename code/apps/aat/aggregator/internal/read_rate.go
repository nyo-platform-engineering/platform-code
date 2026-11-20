package internal

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Each handler has its own bucket: replicas do not coordinate or share tokens.
type readBucket struct {
	mu          sync.Mutex
	rate, burst float64
	tokens      float64
	last        time.Time
}

func (b *readBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// time.Now carries a monotonic clock, so worker wall-clock skew does not
	// refill the bucket. Ignore older timestamps from concurrent callers.
	if now.After(b.last) {
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func limitReadRate(rps, burst int) gin.HandlerFunc {
	if rps <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	if burst < 1 {
		panic("read rate burst must be positive")
	}
	bucket := &readBucket{rate: float64(rps), burst: float64(burst), tokens: float64(burst), last: time.Now()}
	return func(c *gin.Context) {
		c.Header("X-Read-RPS-Limit", strconv.Itoa(rps))
		c.Header("X-Read-RPS-Burst", strconv.Itoa(burst))
		if !bucket.allow(time.Now()) {
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "read rate limit reached; retry later"})
			return
		}
		c.Next()
	}
}
