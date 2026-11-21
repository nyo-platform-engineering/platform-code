package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
)

// hangingSensor accepts connections and then never finishes answering: "head" stalls
// before the response headers, "body" sends headers and part of the body, then stalls.
// It counts requests and the most connections held open at once.
type hangingSensor struct {
	mode     string
	hanging  atomic.Bool
	requests atomic.Int32
	inFlight atomic.Int32
	maxOpen  atomic.Int32
}

func (h *hangingSensor) handler(healthy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.hanging.Load() {
			healthy.ServeHTTP(w, r)
			return
		}
		h.requests.Add(1)
		open := h.inFlight.Add(1)
		defer h.inFlight.Add(-1)
		for {
			seen := h.maxOpen.Load()
			if open <= seen || h.maxOpen.CompareAndSwap(seen, open) {
				break
			}
		}
		if h.mode == "body" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"generated_at":"2026-11-12T00:31:00Z","items":[`))
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done() // released only when the client gives up
	})
}

// P5 criterion 3: a sensor that accepts connections but never answers must not slow
// the other sources. The sensor client gives up at its own timeout, holds at most two
// connections, opens the breaker after three hangs and then stops calling, while BMKG
// polls running at the same time stay within the M1 latency target (p95 < 300 ms).
// It recovers without intervention once the sensor answers again.
func TestHangingSensorIsBoundedAndDoesNotSlowOtherSources(t *testing.T) {
	for _, mode := range []string{"head", "body"} {
		t.Run(mode, func(t *testing.T) {
			ctrl := pollController(t)

			bmkg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/seismic-events" {
					_, _ = w.Write([]byte(`[{"event_id":"eq-1","magnitude":5,"depth_km":10,"epicenter_lat":-8,"epicenter_lon":110,"region_name":"Jawa","occurred_at":"2026-09-25T00:00:00Z","potential_tsunami":false}]`))
					return
				}
				_, _ = w.Write([]byte(`[]`))
			}))
			defer bmkg.Close()
			bmkgPoller := newSourcePoller("BMKG", bmkg.URL, "X-BMKG-Key", "key",
				[]pollEndpoint{{"/seismic-events", "seismic_events"}, {"/tsunami-warnings", "tsunami_warnings"}}, ctrl, bmkg.Client())

			healthy := &fakeSensor{pages: []string{
				`{"generated_at":"2026-11-12T00:31:00Z","next_cursor":"p1","items":[` + observation("obs-1", `,"critical":false`) + `]}`,
			}}
			sensor := &hangingSensor{mode: mode}
			sensor.hanging.Store(true)
			server := httptest.NewServer(sensor.handler(healthy.handler(t)))
			defer server.Close()

			session, err := ctrl.AcquirePollSession(context.Background(), controller.SensorSource)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			// Production uses SENSOR_TIMEOUT_MS=2000; a shorter timeout keeps the test fast
			// and the behaviour is the same.
			const timeout = 400 * time.Millisecond
			p := newSensorPoller(server.URL, "pantai", ctrl, timeout)

			// Sensor polling runs like the real poller: its own goroutine, one step per tick.
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			var steps []time.Duration
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ctx.Err() == nil {
					started := time.Now()
					p.step(context.Background(), session.Controller)
					steps = append(steps, time.Since(started))
					select {
					case <-ctx.Done():
					case <-time.After(50 * time.Millisecond):
					}
				}
			}()

			// Meanwhile BMKG keeps polling and storing.
			var bmkgPolls []time.Duration
			deadline := time.Now().Add(6 * timeout) // three hung calls, before the ≥1.5 s open period ends
			for time.Now().Before(deadline) {
				started := time.Now()
				if err := bmkgPoller.Poll(context.Background()); err != nil {
					t.Fatalf("BMKG poll failed while the sensor hangs: %v", err)
				}
				bmkgPolls = append(bmkgPolls, time.Since(started))
				time.Sleep(20 * time.Millisecond)
			}
			cancel()
			wg.Wait()

			sort.Slice(bmkgPolls, func(i, j int) bool { return bmkgPolls[i] < bmkgPolls[j] })
			p95 := bmkgPolls[(len(bmkgPolls)*95+99)/100-1]
			if p95 >= 300*time.Millisecond {
				t.Fatalf("BMKG poll p95 %v while the sensor hangs; M1 target is < 300 ms (%d polls)", p95, len(bmkgPolls))
			}
			state, err := ctrl.GetPollState(context.Background(), "BMKG")
			if err != nil || !state.Healthy {
				t.Fatalf("BMKG must stay healthy while the sensor hangs: %+v %v", state, err)
			}

			for i, d := range steps {
				if d > timeout+300*time.Millisecond {
					t.Fatalf("sensor step %d took %v; a hung call must give up at the %v timeout", i, d, timeout)
				}
			}
			if got := sensor.maxOpen.Load(); got > 2 {
				t.Fatalf("the sensor client held %d connections open; the bulkhead allows 2", got)
			}
			if p.breaker.state != "open" && p.breaker.state != "half_open" {
				t.Fatalf("three hung calls must open the breaker, got %s after %d steps", p.breaker.state, len(steps))
			}
			if sent := sensor.requests.Load(); sent != 3 {
				t.Fatalf("%d requests reached the hung sensor in %d steps; the breaker must stop after 3", sent, len(steps))
			}

			// Recovery without intervention: once the open period passes, one trial closes it.
			sensor.hanging.Store(false)
			p.breaker.openUntil = time.Now()
			p.step(context.Background(), session.Controller)
			if p.breaker.state != "closed" || p.lastSuccess == nil {
				t.Fatalf("breaker did not close after the sensor answered again: %s", p.breaker.state)
			}
		})
	}
}
