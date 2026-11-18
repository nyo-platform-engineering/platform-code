package api

import (
	"net/http"
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
