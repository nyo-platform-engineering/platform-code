// Local stand-in for the assistants' Satellite Feed, for development without the
// group token. It follows the published interface (GET /v1/session, SSE
// GET /v1/stream with detection/update/retract/heartbeat events, gapless offsets,
// one connection per token) and adds the awkward behaviour the real feed is
// described as having: cross-satellite duplicates, re-sent detections, late
// arrivals, updates, retractions, and dropped connections.
//
// The payload shape is an assumption until it is checked against the real feed.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type event struct {
	Offset int64
	Kind   string
	Data   []byte
}

type detection struct {
	Satellite  string
	At         time.Time
	Lat, Lon   float64
	Confidence string
	FRP        float64
}

func (d detection) payload(kind string) []byte {
	fields := []string{
		fmt.Sprintf(`"satellite":%q`, d.Satellite),
		fmt.Sprintf(`"acq_date":%q`, d.At.Format("2006-01-02")),
		fmt.Sprintf(`"acq_time":%q`, d.At.Format("1504")),
		// Fixed five decimals, as FIRMS writes them; detection keys keep this text.
		fmt.Sprintf(`"latitude":%.5f`, d.Lat),
		fmt.Sprintf(`"longitude":%.5f`, d.Lon),
	}
	if kind != "retract" {
		fields = append(fields, fmt.Sprintf(`"confidence":%q`, d.Confidence), fmt.Sprintf(`"frp":%.2f`, d.FRP))
	}
	return []byte("{" + strings.Join(fields, ",") + "}")
}

type fire struct {
	Lat, Lon float64
	Until    time.Time
}

type feed struct {
	mu sync.Mutex

	token       string
	rng         *rand.Rand
	passEvery   time.Duration // virtual time between satellite passes
	perPass     int
	dropAfter   time.Duration
	heartbeat   time.Duration
	speed       float64
	anchorReal  time.Time
	anchorVirt  time.Time
	nextPass    time.Time
	pass        int
	events      []event
	history     []detection // emitted detections, for updates and retractions
	fires       []fire
	delayed     map[int][]detection // pass number -> detections arriving late
	connected   bool
	dropCurrent chan struct{}
}

func (f *feed) virtualNow() time.Time {
	return f.anchorVirt.Add(time.Duration(float64(time.Since(f.anchorReal)) * f.speed))
}

// advance generates every pass whose virtual time has come. Callers hold f.mu.
func (f *feed) advance() {
	now := f.virtualNow()
	for !f.nextPass.After(now) {
		f.generatePass(f.nextPass)
		f.nextPass = f.nextPass.Add(f.passEvery)
		f.pass++
	}
}

func (f *feed) emit(kind string, data []byte) {
	f.events = append(f.events, event{Offset: int64(len(f.events)) + 1, Kind: kind, Data: data})
}

var regions = [][4]float64{ // lat min, lat max, lon min, lon max: peatland areas
	{-3.5, -1.0, 111.0, 115.5}, // Kalimantan Tengah
	{-3.8, -2.0, 104.0, 106.0}, // Sumatera Selatan
	{0.0, 2.0, 100.5, 103.0},   // Riau
	{-1.8, -0.5, 109.2, 111.0}, // Kalimantan Barat
}

func (f *feed) generatePass(at time.Time) {
	r := f.rng
	satellites := []string{"N20", "N21", "NPP"}
	sat := satellites[f.pass%len(satellites)]
	at = at.UTC().Truncate(time.Minute)

	// Keep a handful of burning fires; old ones go out, new ones start.
	alive := f.fires[:0]
	for _, x := range f.fires {
		if x.Until.After(at) {
			alive = append(alive, x)
		}
	}
	f.fires = alive
	for len(f.fires) < 6 {
		region := regions[r.IntN(len(regions))]
		f.fires = append(f.fires, fire{
			Lat:   region[0] + r.Float64()*(region[1]-region[0]),
			Lon:   region[2] + r.Float64()*(region[3]-region[2]),
			Until: at.Add(time.Duration(2+r.IntN(20)) * time.Hour),
		})
	}

	batch := []detection{}
	for len(batch) < f.perPass {
		if r.Float64() < 0.6 {
			x := f.fires[r.IntN(len(f.fires))]
			batch = append(batch, near(r, sat, at, x.Lat, x.Lon, 0.4))
			if r.Float64() < 0.15 { // the same fire seen by a second satellite
				other := satellites[(f.pass+1)%len(satellites)]
				batch = append(batch, near(r, other, at.Add(2*time.Minute), x.Lat, x.Lon, 0.4))
			}
			continue
		}
		// Hot roofs and noise: scattered single detections, mostly low confidence.
		region := regions[r.IntN(len(regions))]
		d := near(r, sat, at, region[0]+r.Float64()*(region[1]-region[0]), region[2]+r.Float64()*(region[3]-region[2]), 0)
		if r.Float64() < 0.7 {
			d.Confidence = "l"
		}
		batch = append(batch, d)
	}

	// Detections held back from earlier passes arrive now, ahead of this pass.
	for _, d := range f.delayed[f.pass] {
		f.emit("detection", d.payload("detection"))
		f.history = append(f.history, d)
	}
	delete(f.delayed, f.pass)

	for _, d := range batch {
		switch p := r.Float64(); {
		case p < 0.04: // arrives a few passes late
			later := f.pass + 1 + r.IntN(4)
			f.delayed[later] = append(f.delayed[later], d)
			continue
		case p < 0.05: // arrives far beyond any reasonable lateness
			later := f.pass + int((8*time.Hour)/f.passEvery)
			f.delayed[later] = append(f.delayed[later], d)
			continue
		}
		f.emit("detection", d.payload("detection"))
		f.history = append(f.history, d)
		if r.Float64() < 0.03 { // re-sent unchanged
			f.emit("detection", d.payload("detection"))
		}
	}

	if len(f.history) > 0 {
		for range max(1, f.perPass/30) {
			i := len(f.history) - 1 - r.IntN(min(len(f.history), 200))
			d := f.history[i]
			if r.Float64() < 0.6 {
				d.FRP = math.Round((d.FRP+r.Float64()*8)*100) / 100
				if d.Confidence == "n" {
					d.Confidence = "h"
				}
				f.history[i] = d
				f.emit("update", d.payload("update"))
			} else {
				f.emit("retract", d.payload("retract"))
			}
		}
	}
	if len(f.history) > 5000 {
		f.history = append([]detection(nil), f.history[len(f.history)-2000:]...)
	}
}

func near(r *rand.Rand, sat string, at time.Time, lat, lon, radiusKm float64) detection {
	angle := r.Float64() * 2 * math.Pi
	dist := r.Float64() * radiusKm
	conf := "n"
	if r.Float64() < 0.35 {
		conf = "h"
	}
	return detection{
		Satellite:  sat,
		At:         at,
		Lat:        lat + dist/111*math.Sin(angle),
		Lon:        lon + dist/(111*math.Cos(lat*math.Pi/180))*math.Cos(angle),
		Confidence: conf,
		FRP:        math.Round((0.5+r.ExpFloat64()*6)*100) / 100,
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (f *feed) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		writeError(w, http.StatusUnauthorized, "unauthorized", "token tidak valid")
		return false
	}
	return true
}

func (f *feed) session(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	f.mu.Lock()
	f.advance()
	body := map[string]any{
		"group": "local", "profile": "mock", "speed": f.speed,
		"virtual_now": f.virtualNow().UTC().Format(time.RFC3339), "latest_offset": int64(len(f.events)),
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (f *feed) stream(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	// next is the first offset to send: from is inclusive, Last-Event-ID exclusive.
	next := int64(1)
	if text := r.URL.Query().Get("from"); text != "" {
		from, err := strconv.ParseInt(text, 10, 64)
		if err != nil || from < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "from tidak valid")
			return
		}
		next = max(next, from)
	}
	if text := r.Header.Get("Last-Event-ID"); text != "" {
		last, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Last-Event-ID tidak valid")
			return
		}
		next = max(next, last+1)
	}

	f.mu.Lock()
	if f.connected {
		f.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "too_many_connections", "satu koneksi per grup")
		return
	}
	f.connected = true
	drop := make(chan struct{})
	f.dropCurrent = drop
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.connected = false
		f.dropCurrent = nil
		f.mu.Unlock()
	}()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	var deadline <-chan time.Time
	if f.dropAfter > 0 {
		deadline = time.After(time.Duration(float64(f.dropAfter) * (0.5 + rand.Float64())))
	}
	heartbeat := time.NewTicker(f.heartbeat)
	defer heartbeat.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		f.mu.Lock()
		f.advance()
		pending := append([]event(nil), f.events[min(int(next-1), len(f.events)):]...)
		f.mu.Unlock()
		for _, e := range pending {
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Offset, e.Kind, e.Data); err != nil {
				return
			}
			next = e.Offset + 1
		}
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-drop:
			slog.Info("stream dropped on request", "next", next)
			return
		case <-deadline:
			slog.Info("stream dropped", "next", next)
			return
		case <-heartbeat.C:
			f.mu.Lock()
			now := f.virtualNow().UTC().Format(time.RFC3339)
			f.mu.Unlock()
			fmt.Fprintf(w, "event: heartbeat\ndata: {\"virtual_now\":%q}\n\n", now)
		case <-tick.C:
		}
	}
}

// POST /admin/speed {"speed":60} changes the virtual clock rate without a jump.
func (f *feed) setSpeed(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var body struct {
		Speed float64 `json:"speed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Speed <= 0 || body.Speed > 1000 {
		writeError(w, http.StatusBadRequest, "bad_request", "speed harus 0 < speed <= 1000")
		return
	}
	f.mu.Lock()
	f.advance()
	f.anchorVirt, f.anchorReal, f.speed = f.virtualNow(), time.Now(), body.Speed
	f.mu.Unlock()
	slog.Info("speed changed", "speed", body.Speed)
	w.WriteHeader(http.StatusNoContent)
}

// POST /admin/drop closes the open stream, to test reconnecting.
func (f *feed) drop(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	f.mu.Lock()
	if f.dropCurrent != nil {
		close(f.dropCurrent)
		f.dropCurrent = nil
	}
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	token := os.Getenv("SATELLITE_GROUP_TOKEN")
	if token == "" {
		panic("SATELLITE_GROUP_TOKEN is required")
	}
	start := time.Now().UTC().Add(-2 * time.Hour)
	if text := os.Getenv("START_TIME"); text != "" {
		parsed, err := time.Parse(time.RFC3339, text)
		if err != nil {
			panic("START_TIME must be RFC3339")
		}
		start = parsed.UTC()
	}
	speed, err := strconv.ParseFloat(os.Getenv("SPEED"), 64)
	if err != nil || speed <= 0 {
		speed = 1
	}
	f := &feed{
		token:      token,
		rng:        rand.New(rand.NewPCG(uint64(envInt("SEED", 4031)), 2026)),
		passEvery:  time.Duration(envInt("PASS_INTERVAL_SECONDS", 120)) * time.Second,
		perPass:    envInt("DETECTIONS_PER_PASS", 40),
		dropAfter:  time.Duration(envInt("DROP_AFTER_SECONDS", 0)) * time.Second,
		heartbeat:  time.Duration(envInt("HEARTBEAT_SECONDS", 5)) * time.Second,
		speed:      speed,
		anchorReal: time.Now(),
		// The virtual clock starts at the first pass so history is generated
		// immediately and then continues in real time times speed.
		anchorVirt: time.Now().UTC(),
		nextPass:   start,
		delayed:    map[int][]detection{},
	}
	go func() { // generate passes even when nobody is connected, so lag builds up
		for range time.Tick(200 * time.Millisecond) {
			f.mu.Lock()
			f.advance()
			f.mu.Unlock()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })
	mux.HandleFunc("GET /v1/session", f.session)
	mux.HandleFunc("GET /v1/stream", f.stream)
	mux.HandleFunc("POST /admin/speed", f.setSpeed)
	mux.HandleFunc("POST /admin/drop", f.drop)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8087"
	}
	slog.Info("satellite mock listening", "port", port, "speed", speed)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		panic(err)
	}
}
