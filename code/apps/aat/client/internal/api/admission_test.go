package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Under overload Media is shed while the Field Team is still served.
func TestAdmissionShedsMediaBeforeFieldTeam(t *testing.T) {
	release := make(chan struct{})
	var held sync.WaitGroup
	var introspections atomic.Int32
	h, err := Handler(Config{AuthURL: "http://auth", AggregatorURL: "http://aggregator", AuthToken: "auth-internal", AggregatorToken: "agg-internal",
		MaxConcurrent: 4, MediaSharePercent: 50, Timeout: 2 * time.Second, StaleAfter: time.Minute,
		HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "auth" {
				introspections.Add(1)
				if r.ContentLength > 0 {
					buf := make([]byte, r.ContentLength)
					_, _ = r.Body.Read(buf)
					if string(buf) == `{"token":"field"}` {
						return response(200, `{"active":true,"client_id":"responder"}`), nil
					}
				}
				return response(200, `{"active":true,"client_id":"public"}`), nil
			}
			if r.URL.Path == "/internal/hazards" && r.URL.Query().Get("limit") == "1" {
				held.Done()
				<-release // a slow Media request keeps its slot
			}
			if r.URL.Path == "/internal/source-status" {
				return response(200, `{"sources":[]}`), nil
			}
			return response(200, hazardPage), nil
		})}})
	if err != nil {
		t.Fatal(err)
	}

	// Two slow Media requests fill the media share (50% of 4).
	held.Add(2)
	done := make(chan int, 2)
	for range 2 {
		go func() { done <- invoke(h, "/hazards?limit=1", "Bearer media").Code }()
	}
	held.Wait()

	if w := invoke(h, "/hazards", "Bearer media"); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("a third Media request must be shed, got %d", w.Code)
	}
	for range 3 {
		if w := invoke(h, "/hazards", "Bearer field"); w.Code != 200 {
			t.Fatalf("the Field Team must still be served, got %d %s", w.Code, w.Body.String())
		}
	}
	close(release)
	for range 2 {
		if code := <-done; code != 200 {
			t.Fatalf("held Media request finished with %d", code)
		}
	}
	if w := invoke(h, "/hazards", "Bearer media"); w.Code != 200 {
		t.Fatalf("Media is served again once load drops, got %d", w.Code)
	}
	// Repeated tokens are classified from the cache, not re-introspected each time.
	if n := introspections.Load(); n > 4 {
		t.Fatalf("expected cached introspections, got %d calls", n)
	}
}

// Reads for the Field Team are marked priority for the Aggregator's reserved slots and
// retried once when the Aggregator still answers 429; Media reads are neither.
func TestPriorityReadsAreMarkedAndRetriedOnce(t *testing.T) {
	var mu sync.Mutex
	calls := map[string][]string{}                 // caller -> X-Read-Class of each /internal/hazards call
	busy := map[string]int{"field": 1, "media": 1} // how many 429s the Aggregator answers first
	h, err := Handler(Config{AuthURL: "http://auth", AggregatorURL: "http://aggregator", AuthToken: "auth-internal", AggregatorToken: "agg-internal",
		MaxConcurrent: 4, MediaSharePercent: 50, Timeout: 2 * time.Second, StaleAfter: time.Minute,
		HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "auth" {
				buf := make([]byte, r.ContentLength)
				_, _ = r.Body.Read(buf)
				if string(buf) == `{"token":"field"}` {
					return response(200, `{"active":true,"client_id":"responder"}`), nil
				}
				return response(200, `{"active":true,"client_id":"public"}`), nil
			}
			if r.URL.Path == "/internal/source-status" {
				return response(200, `{"sources":[]}`), nil
			}
			caller := map[string]string{"7": "field", "8": "media", "9": "spoof"}[r.URL.Query().Get("limit")]
			mu.Lock()
			defer mu.Unlock()
			calls[caller] = append(calls[caller], r.Header.Get("X-Read-Class"))
			if busy[caller] > 0 {
				busy[caller]--
				return response(429, `{"error":"read capacity reached; retry later"}`), nil
			}
			return response(200, hazardPage), nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	if w := invoke(h, "/hazards?limit=7", "Bearer field"); w.Code != 200 {
		t.Fatalf("a Field Team read that meets one 429 is retried and served, got %d %s", w.Code, w.Body.String())
	}
	if got := calls["field"]; len(got) != 2 || got[0] != "priority" || got[1] != "priority" {
		t.Fatalf("Field Team reads must be marked priority and retried exactly once: %v", got)
	}
	if w := invoke(h, "/hazards?limit=8", "Bearer media"); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("a Media read that meets 429 is shed, not retried: %d", w.Code)
	}
	if got := calls["media"]; len(got) != 1 || got[0] != "" {
		t.Fatalf("Media reads are not marked and not retried: %v", got)
	}
	// A caller cannot claim priority by sending the header itself.
	r := httptest.NewRequest(http.MethodGet, "/hazards?limit=9", nil)
	r.Header.Set("Authorization", "Bearer media")
	r.Header.Set("X-Read-Class", "priority")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got := calls["spoof"]; len(got) != 1 || got[0] != "" {
		t.Fatalf("a caller's own X-Read-Class must not reach the Aggregator: %v", got)
	}
}
