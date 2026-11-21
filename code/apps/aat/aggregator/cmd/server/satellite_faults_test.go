package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
)

// P4 criterion 8: the connection to the feed can fail in ways other than a clean
// close. These tests drive the real consumer (Run) against a scripted feed and check
// that each failure ends in a reconnect from the committed offset, with the same final
// state as one uninterrupted pass over the stream: an offset gap, a stream that goes
// silent (half-open connection), and 503 answers.

// scriptedFeed serves feedScript. Each connection is handled by behave(n, from, w, r),
// n counting from 1; it records the from offset of every connection.
type scriptedFeed struct {
	mu    sync.Mutex
	froms []int
	times []time.Time
}

func (f *scriptedFeed) server(t *testing.T, behave func(n, from int, w http.ResponseWriter, r *http.Request)) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/session" {
			_, _ = fmt.Fprintf(w, `{"latest_offset":%d,"virtual_now":"2026-11-12T03:00:00Z","speed":1}`, len(feedScript)-1)
			return
		}
		from, _ := strconv.Atoi(r.URL.Query().Get("from"))
		f.mu.Lock()
		f.froms = append(f.froms, from)
		f.times = append(f.times, time.Now())
		n := len(f.froms)
		f.mu.Unlock()
		behave(n, from, w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func (f *scriptedFeed) connections() ([]int, []time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.froms...), append([]time.Time(nil), f.times...)
}

// send writes feedScript events with the given offsets as SSE.
func send(w http.ResponseWriter, offsets ...int) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, i := range offsets {
		e := feedScript[i]
		_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", i, e.kind, e.data)
	}
	w.(http.Flusher).Flush()
}

func span(from, to int) []int {
	out := []int{}
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}

// liveTail keeps a caught-up stream open with keep-alive comments, as the real feed
// does, so the idle watchdog has no reason to reconnect.
func liveTail(w http.ResponseWriter, r *http.Request) {
	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(50 * time.Millisecond):
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			w.(http.Flusher).Flush()
		}
	}
}

// consumeAll runs the consumer until the last event is committed, then stops it.
func consumeAll(t *testing.T, ctrl *controller.Controller, consumer satelliteConsumer, within time.Duration) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	started := time.Now()
	go func() { consumer.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	last := int64(len(feedScript) - 1)
	for {
		offset, err := ctrl.SatelliteFeedOffset(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if offset != nil && *offset == last {
			return time.Since(started)
		}
		if time.Since(started) > within {
			t.Fatalf("consumer stuck at offset %s after %v", offsetText(offset), within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// requireSameAsUninterrupted compares the fires with one uninterrupted batch of the
// whole stream on a fresh schema.
func requireSameAsUninterrupted(t *testing.T, ctrl *controller.Controller) {
	t.Helper()
	reference := pollController(t)
	session, err := reference.AcquirePollSession(context.Background(), controller.SatelliteSource)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Controller.IngestFeedBatch(context.Background(), feedEvents()); err != nil {
		t.Fatal(err)
	}
	got, want := satelliteFires(t, ctrl), satelliteFires(t, reference)
	if len(got) == 0 || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("state after the fault differs from an uninterrupted run:\n got %+v\nwant %+v", got, want)
	}
	counts, err := ctrl.SatelliteCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantCounts, err := reference.SatelliteCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(counts) != fmt.Sprint(wantCounts) {
		t.Fatalf("counts after the fault differ: got %+v, want %+v", counts, wantCounts)
	}
}

// The feed skips offsets 3 and 4. Nothing after the gap may be applied: the consumer
// commits what it has (0–2), drops the connection, and reconnects from offset 2.
func TestSatelliteFeedGapReconnectsFromLastGoodOffset(t *testing.T) {
	ctrl := pollController(t)
	feed := &scriptedFeed{}
	server := feed.server(t, func(n, from int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			send(w, 0, 1, 2, 5, 6) // 3 and 4 never arrive on this connection
		} else {
			send(w, span(from, len(feedScript))...)
		}
		liveTail(w, r)
	})
	consumer := newSatelliteConsumer(server.URL, "token", ctrl)
	consumer.flushEvery = 20 * time.Millisecond
	consumeAll(t, ctrl, consumer, 20*time.Second)

	froms, _ := feed.connections()
	if len(froms) != 2 || froms[0] != 0 || froms[1] != 2 {
		t.Fatalf("connections from %v; want [0 2]: one reconnect from the last offset before the gap", froms)
	}
	requireSameAsUninterrupted(t, ctrl)
}

// The connection stays open but goes silent: no events, no heartbeats (a half-open
// connection). The idle watchdog must reconnect from the committed offset; a stream
// that sends only keep-alive comments must not be treated as idle.
func TestSatelliteSilentStreamReconnectsAfterIdleTimeout(t *testing.T) {
	ctrl := pollController(t)
	feed := &scriptedFeed{}
	var silentAt time.Time
	server := feed.server(t, func(n, from int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			send(w, 0, 1, 2, 3)
			feed.mu.Lock()
			silentAt = time.Now()
			feed.mu.Unlock()
			<-r.Context().Done() // open, but nothing more is ever written
			return
		}
		send(w, span(from, len(feedScript))...)
		liveTail(w, r)
	})
	const idle = 400 * time.Millisecond
	consumer := newSatelliteConsumer(server.URL, "token", ctrl)
	consumer.flushEvery = 20 * time.Millisecond
	consumer.idleTimeout = idle
	consumeAll(t, ctrl, consumer, 20*time.Second)

	// Stay connected well past the idle timeout: heartbeats alone keep the stream alive.
	time.Sleep(4 * idle)
	froms, times := feed.connections()
	if len(froms) != 2 || froms[1] != 3 {
		t.Fatalf("connections from %v; want exactly one reconnect, from offset 3", froms)
	}
	feed.mu.Lock()
	waited := times[1].Sub(silentAt)
	feed.mu.Unlock()
	if waited < idle {
		t.Fatalf("reconnected %v after the stream went silent, before the %v idle timeout", waited, idle)
	}
	requireSameAsUninterrupted(t, ctrl)
}

// 503 means the feed is temporarily unavailable: retry with the normal backoff (not
// the 30 s pause for a rejected token) and resume without loss once it answers.
func TestSatelliteFeedUnavailableIsRetried(t *testing.T) {
	c := newSatelliteConsumer("http://unused", "token", nil)
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"feed sedang tidak tersedia"}}`))
	}))
	c.baseURL = unavailable.URL
	_, err := c.open(context.Background(), nil, "test")
	unavailable.Close()
	if err == nil || errors.Is(err, errFeedRejected) || errors.Is(err, errFeedThrottled) {
		t.Fatalf("503 must be a retryable error, not rejected or throttled: %v", err)
	}

	ctrl := pollController(t)
	feed := &scriptedFeed{}
	server := feed.server(t, func(n, from int, w http.ResponseWriter, r *http.Request) {
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"feed sedang tidak tersedia"}}`))
			return
		}
		send(w, span(from, len(feedScript))...)
		liveTail(w, r)
	})
	consumer := newSatelliteConsumer(server.URL, "token", ctrl)
	consumer.flushEvery = 20 * time.Millisecond
	took := consumeAll(t, ctrl, consumer, 15*time.Second)

	froms, _ := feed.connections()
	if len(froms) != 3 || froms[2] != 0 {
		t.Fatalf("connections from %v; want two 503s, then a stream from offset 0", froms)
	}
	// Backoff 1 s then 2 s with jitter: at most 3 s of waiting, far below the 30 s pause.
	if took > 10*time.Second {
		t.Fatalf("recovery after two 503s took %v", took)
	}
	requireSameAsUninterrupted(t, ctrl)
}
