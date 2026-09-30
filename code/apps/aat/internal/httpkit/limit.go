package httpkit

import "net/http"

// Limit rejects excess active requests without queueing. The bound is per process.
func Limit(max int, next http.Handler) http.Handler {
	if max < 1 {
		panic("concurrency limit must be positive")
	}
	slots := make(chan struct{}, max)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			JSON(w, http.StatusTooManyRequests, map[string]string{"error": "concurrency limit reached"})
		}
	})
}
