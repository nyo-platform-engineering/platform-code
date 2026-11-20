package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
)

func TestBreakerOpensBacksOffAndRecovers(t *testing.T) {
	b := newBreaker(3, time.Second, 8*time.Second)
	b.random = func() float64 { return 0.5 } // no jitter: open periods are exact
	now := time.Unix(0, 0)
	for range 2 {
		b.failure(now, "503")
	}
	if b.state != "closed" || !b.allow(now) {
		t.Fatal("two failures must not open the breaker")
	}
	b.failure(now, "503")
	if b.state != "open" || b.allow(now.Add(999*time.Millisecond)) {
		t.Fatal("the third failure opens the breaker and blocks requests")
	}
	if !b.allow(now.Add(time.Second)) || b.state != "half_open" {
		t.Fatal("after the open period one trial request is allowed")
	}
	b.failure(now.Add(time.Second), "503")
	if b.state != "open" || b.openUntil != now.Add(3*time.Second) {
		t.Fatalf("a failed trial reopens for twice as long, got %s until %v", b.state, b.openUntil)
	}
	for i := range 6 { // repeated failed trials stop growing at maxOpen
		at := b.openUntil
		b.allow(at)
		b.failure(at, "503")
		if i == 5 && b.openUntil.Sub(at) != 8*time.Second {
			t.Fatalf("open period must cap at 8s, got %v", b.openUntil.Sub(at))
		}
	}
	b.allow(b.openUntil)
	b.success()
	if b.state != "closed" || b.opens != 0 {
		t.Fatal("a successful trial closes the breaker and resets the backoff")
	}
}

func TestSensorFreshnessFlagsFrozenAndOldData(t *testing.T) {
	p := &sensorPoller{staleAfter: 15 * time.Minute, frozenAfter: -1} // any wait counts as frozen
	at := func(m int) time.Time { return time.Date(2026, 11, 12, 0, m, 0, 0, time.UTC) }
	items := func(ts ...time.Time) func(func(time.Time) bool) {
		return func(yield func(time.Time) bool) {
			for _, t := range ts {
				if !yield(t) {
					return
				}
			}
		}
	}
	p.observeFreshness(at(10), false, items(at(9), at(10)))
	if p.stale {
		t.Fatal("current data is fresh")
	}
	p.observeFreshness(at(10), false, items())
	if !p.stale || !strings.Contains(p.staleReason, "frozen") {
		t.Fatalf("a sensor clock that stops moving is stale: %q", p.staleReason)
	}
	p.observeFreshness(at(40), false, items())
	if !p.stale || !strings.Contains(p.staleReason, "behind") {
		t.Fatalf("200 OK with no reading for 30 sensor-minutes is stale: %q", p.staleReason)
	}
	p.observeFreshness(at(41), true, items(at(1)))
	if p.stale {
		t.Fatal("reading an old backlog page is catching up, not stale")
	}
	p.observeFreshness(at(42), false, items(at(42)))
	if p.stale {
		t.Fatal("fresh data clears the flag without intervention")
	}
}

func TestSensorRestartWithEarlierClockIsNotFrozen(t *testing.T) {
	p := &sensorPoller{staleAfter: 15 * time.Minute, frozenAfter: -1} // any wait counts as frozen
	at := func(h, m int) time.Time { return time.Date(2026, 11, 12, h, m, 0, 0, time.UTC) }
	items := func(ts ...time.Time) func(func(time.Time) bool) {
		return func(yield func(time.Time) bool) {
			for _, t := range ts {
				if !yield(t) {
					return
				}
			}
		}
	}
	// The sensor ran for 32 sensor-hours, then restarted and replays from 00:00.
	p.observeFreshness(at(32, 0), false, items(at(31, 59), at(32, 0)))
	p.observeFreshness(at(0, 5), false, items(at(0, 4), at(0, 5)))
	if p.stale {
		t.Fatalf("a restarted sensor whose clock went back is live, not frozen: %q", p.staleReason)
	}
	if !p.lastGenerated.Equal(at(0, 5)) || !p.newestObserved.Equal(at(0, 5)) {
		t.Fatalf("freshness restarts from the new clock: generated %s, newest %s", p.lastGenerated, p.newestObserved)
	}
	// Staleness is still detected after the restart, against the new clock.
	p.observeFreshness(at(0, 5), false, items())
	if !p.stale || !strings.Contains(p.staleReason, "frozen") {
		t.Fatalf("a restarted sensor whose clock then stops is stale: %q", p.staleReason)
	}
	p.observeFreshness(at(0, 40), false, items())
	if !p.stale || !strings.Contains(p.staleReason, "behind") {
		t.Fatalf("no reading for 35 sensor-minutes after a restart is stale: %q", p.staleReason)
	}
}

// fakeSensor serves the Lampiran C contract. Observations are served one page at a
// time; failing makes every call answer 503.
type fakeSensor struct {
	pages    []string
	failing  atomic.Bool
	requests atomic.Int32
}

func (f *fakeSensor) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer pantai" {
			w.WriteHeader(401)
			return
		}
		if f.failing.Load() {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"sensor sedang tidak melayani"}}`))
			return
		}
		switch r.URL.Path {
		case "/v1/stations":
			_, _ = w.Write([]byte(`{"items":[{"station_id":"stn-padang-01","name":"Padang","province":"Sumatera Barat","latitude":-0.9492,"longitude":100.3543}]}`))
		case "/v1/observations":
			page := 0
			fmt.Sscanf(r.URL.Query().Get("cursor"), "p%d", &page)
			if page >= len(f.pages) {
				_, _ = fmt.Fprintf(w, `{"generated_at":"2026-11-12T01:00:00Z","items":[],"next_cursor":"p%d"}`, page)
				return
			}
			_, _ = w.Write([]byte(f.pages[page]))
		default:
			w.WriteHeader(404)
		}
	})
}

func observation(id, extra string) string {
	return `{"observation_id":"` + id + `","station_id":"stn-padang-01","observed_at":"2026-11-12T00:30:00Z","quality":"ok","sea_level_anomaly_m":0.1,"wave_height_m":1.2` + extra + `}`
}

func TestSensorPollerParksViolationsAndAlertsEveryCriticalReading(t *testing.T) {
	ctrl := pollController(t)
	sensor := &fakeSensor{pages: []string{
		`{"generated_at":"2026-11-12T00:31:00Z","next_cursor":"p1","items":[` + strings.Join([]string{
			observation("obs-1", `,"critical":false`),
			observation("obs-2", `,"critical":true`),
			observation("obs-1", `,"critical":false`), // re-sent unchanged
			strings.Replace(observation("obs-3", `,"critical":false`), `"wave_height_m":1.2`, `"wave_height_m":"1.2"`, 1),
			strings.Replace(observation("obs-4", `,"critical":true`), `"quality":"ok"`, `"quality":"broken"`, 1),
		}, ",") + `]}`,
		`{"generated_at":"2026-11-12T00:32:00Z","next_cursor":"p2","items":[{"observation_id":"obs-5"`, // truncated JSON
	}}
	server := httptest.NewServer(sensor.handler(t))
	defer server.Close()

	session, err := ctrl.AcquirePollSession(context.Background(), controller.SensorSource)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	p := newSensorPoller(server.URL, "pantai", ctrl, time.Second)
	for range 3 {
		p.step(context.Background(), session.Controller)
	}

	var hazards []model.HazardEvent
	if err := ctrl.DB.Where("source = ?", "SENSOR_PANTAI").Order("source_ref_id").Find(&hazards).Error; err != nil {
		t.Fatal(err)
	}
	if len(hazards) != 2 || hazards[0].Ref != "obs-1" || hazards[1].Ref != "obs-2" || hazards[1].Severity != "AWAS" {
		t.Fatalf("only the two valid readings become events, once each: %+v", hazards)
	}
	var parked []model.InboxMessage
	if err := ctrl.DB.Where("source = ? AND status = 'parked'", "SENSOR_PANTAI").Order("sequence").Find(&parked).Error; err != nil {
		t.Fatal(err)
	}
	if len(parked) != 3 {
		t.Fatalf("two broken readings and the truncated response are parked with reasons, got %d", len(parked))
	}
	for _, m := range parked {
		if m.Reason == "" || len(m.Raw) == 0 {
			t.Fatalf("a parked message keeps its raw bytes and reason: %+v", m)
		}
	}
	var lanes []struct {
		Lane  string
		Count int
	}
	if err := ctrl.DB.Raw("SELECT lane, count(*) AS count FROM hazard_outboxes GROUP BY lane ORDER BY lane").Scan(&lanes).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(lanes) != "[{ 1} {critical 1} {critical_only 1}]" {
		t.Fatalf("one regular message, the valid critical one on the critical lane, and an unverified alert for the broken critical reading; got %v", lanes)
	}

	// The truncated page counted as failures; start the outage from a closed breaker.
	if p.breaker.consecutiveTotal == 0 {
		t.Fatal("an unreadable response must count as a failure")
	}
	p.breaker.success()
	// While the sensor fails, requests stop growing once the breaker opens.
	sensor.failing.Store(true)
	before := sensor.requests.Load()
	for range 20 {
		p.step(context.Background(), session.Controller)
	}
	if sent := sensor.requests.Load() - before; sent != 3 || p.breaker.state != "open" {
		t.Fatalf("20 polls during an outage sent %d requests (want 3) with breaker %s", sent, p.breaker.state)
	}
	state, err := ctrl.SensorState(context.Background())
	if err != nil || state.Breaker != "open" || state.OpenUntil == nil {
		t.Fatalf("the open breaker is visible to clients: %+v %v", state, err)
	}

	// Recovery needs no intervention: after the open period one trial closes it.
	// (The truncated page was transient; the sensor now serves it whole.)
	sensor.pages[1] = `{"generated_at":"2026-11-12T00:32:00Z","next_cursor":"p2","items":[` + observation("obs-5", `,"critical":false`) + `]}`
	sensor.failing.Store(false)
	p.breaker.openUntil = time.Now()
	p.step(context.Background(), session.Controller)
	if p.breaker.state != "closed" {
		t.Fatalf("breaker did not close after recovery: %s", p.breaker.state)
	}
	var n int64
	if err := ctrl.DB.Model(&model.HazardEvent{}).Where("source_ref_id = 'obs-5'").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("polling resumes from the stored cursor after recovery: %d %v", n, err)
	}
}
