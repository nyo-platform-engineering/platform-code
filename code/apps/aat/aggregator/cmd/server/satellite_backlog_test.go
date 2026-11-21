package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"aat/aggregator/internal/model"
)

// countingHandler counts the consumer's committed batches and their sizes.
type countingHandler struct {
	slog.Handler
	mu      sync.Mutex
	batches []int64
}

func (h *countingHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "satellite batch committed" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "events" {
				h.mu.Lock()
				h.batches = append(h.batches, a.Value.Int64())
				h.mu.Unlock()
			}
			return true
		})
	}
	return nil
}
func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *countingHandler) WithGroup(string) slog.Handler            { return h }

// A backlog (a burst, or a catch-up) must be committed in full batches. While a batch
// commits, new events queue up and the flush tick fires; the tick used to commit the
// few events already taken as a batch of their own, paying a batch's fixed cost for
// a handful of events. Nothing may be lost or applied twice either way.
func TestSatelliteBacklogIsCommittedInFullBatches(t *testing.T) {
	ctrl := pollController(t)
	const total = 2000
	payload := func(i int) string {
		// Isolated points: cheap to apply, so the test measures batching, not clustering.
		return fmt.Sprintf(`{"detection":{"satellite":"N20","acq_date":"2026-11-12","acq_time":%d,"latitude":%.5f,"longitude":%.5f,"confidence":"n","frp":5}}`,
			i%2400/100*100+i%60, -3+float64(i)*0.0025, 100+float64(i%97)*0.17)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/session" {
			_, _ = fmt.Fprintf(w, `{"latest_offset":%d,"virtual_now":"2026-11-12T23:00:00Z","speed":60}`, total-1)
			return
		}
		next, _ := strconv.Atoi(r.URL.Query().Get("from"))
		w.Header().Set("Content-Type", "text/event-stream")
		for ; next < total; next++ {
			_, _ = fmt.Fprintf(w, "id: %d\nevent: detection\ndata: %s\n\n", next, payload(next))
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	counter := &countingHandler{Handler: slog.Default().Handler()}
	previous := slog.Default()
	slog.SetDefault(slog.New(counter))
	t.Cleanup(func() { slog.SetDefault(previous) })

	consumer := newSatelliteConsumer(server.URL, "token", ctrl)
	consumer.flushEvery = 20 * time.Millisecond // ticks fire during almost every commit
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { consumer.Run(ctx); close(done) }()
	deadline := time.Now().Add(90 * time.Second)
	for {
		offset, err := ctrl.SatelliteFeedOffset(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if offset != nil && *offset == total-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer stuck at offset %v", offset)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	var stored int64
	if err := ctrl.DB.Model(&model.SatelliteDetection{}).Count(&stored).Error; err != nil || stored != total {
		t.Fatalf("stored %d detections, want %d (%v)", stored, total, err)
	}
	counter.mu.Lock()
	defer counter.mu.Unlock()
	var sum, small int64
	for _, n := range counter.batches {
		sum += n
		if n < int64(consumer.batchSize)/2 {
			small++
		}
	}
	if sum != total {
		t.Fatalf("batches add up to %d events, want %d", sum, total)
	}
	// 2000 events in batches of 200: 10 full batches, plus at most a couple of partial
	// ones where the stream starts and ends.
	if small > 2 {
		t.Fatalf("%d of %d batches were under half full: %v", small, len(counter.batches), counter.batches)
	}
	t.Logf("%d batches: %v", len(counter.batches), counter.batches)
}
