package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
)

func collectSSE(t *testing.T, stream string) ([]controller.FeedEvent, error) {
	t.Helper()
	var events []controller.FeedEvent
	err := readSSE(strings.NewReader(stream), func(e controller.FeedEvent) error {
		events = append(events, e)
		return nil
	}, func() {})
	return events, err
}

func TestReadSSEParsesEventsAndSkipsKeepAlives(t *testing.T) {
	stream := ": keep-alive\r\n\r\n" +
		"id: 7\r\nevent: detection\r\ndata: {\"a\":\r\ndata: 1}\r\n\r\n" +
		"event: heartbeat\ndata: {\"virtual_now\":\"x\"}\n\n" + // no id: no position, skipped
		"id: 8\nevent: heartbeat\ndata: {}\n\n" +
		"event: update\ndata: {\"offset\":9,\"b\":2}\n\n" // offset carried in the payload
	events, err := collectSSE(t, stream)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("end of stream must be reported, got %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events: %+v", len(events), events)
	}
	if events[0].Offset != 7 || events[0].Type != "detection" || string(events[0].Data) != "{\"a\":\n1}" {
		t.Fatalf("multi-line data not joined: %+v", events[0])
	}
	if events[1].Offset != 8 || events[1].Type != "heartbeat" || events[2].Offset != 9 || events[2].Type != "update" {
		t.Fatalf("unexpected events %+v", events[1:])
	}
}

func TestReadSSERejectsDetectionWithoutOffset(t *testing.T) {
	if _, err := collectSSE(t, "event: detection\ndata: {}\n\n"); err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("a detection without an offset cannot be resumed and must fail, got %v", err)
	}
}

func TestSatelliteOpenClassifiesFeedErrors(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusTooManyRequests: errFeedThrottled,
		http.StatusUnauthorized:    errFeedRejected,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"x","message":"y"}}`))
		}))
		c := newSatelliteConsumer(server.URL, "token", nil)
		_, err := c.open(context.Background(), nil, "test")
		server.Close()
		if !errors.Is(err, want) {
			t.Errorf("status %d: %v, want %v", status, err, want)
		}
	}
}

// feedScript uses the event shapes the assistants' feed sends: detections nested
// under "detection" with numeric acq_time, updates and retractions by key, heartbeats
// with offsets.
var feedScript = []struct {
	kind string
	data string
}{
	{"detection", `{"offset":0,"detection":{"version":"2.0NRT","satellite":"N20","acq_date":"2026-11-12","acq_time":243,"latitude":-2.51230,"longitude":113.80440,"confidence":"h","frp":5,"instrument":"VIIRS"}}`},
	{"detection", `{"offset":1,"detection":{"satellite":"N21","acq_date":"2026-11-12","acq_time":245,"latitude":-2.51500,"longitude":113.80600,"confidence":"n","frp":6}}`},
	// re-sent unchanged
	{"detection", `{"offset":2,"detection":{"satellite":"N20","acq_date":"2026-11-12","acq_time":243,"latitude":-2.51230,"longitude":113.80440,"confidence":"h","frp":5}}`},
	// low confidence, excluded
	{"detection", `{"offset":3,"detection":{"satellite":"N20","acq_date":"2026-11-12","acq_time":243,"latitude":-2.52000,"longitude":113.81000,"confidence":"l","frp":50}}`},
	{"heartbeat", `{"offset":4,"virtual_now":"2026-11-12T03:00:00Z"}`},
	// 1.08 km from the first, 0.78 km from the second: joins through the chain
	{"detection", `{"offset":5,"detection":{"satellite":"N20","acq_date":"2026-11-12","acq_time":300,"latitude":-2.52200,"longitude":113.80700,"confidence":"n","frp":8}}`},
	{"detection", `{"offset":6,"detection":{"satellite":"N20","acq_date":"2026-11-12","acq_time":300,"latitude":-1.00000,"longitude":112.00000,"confidence":"n","frp":5}}`},
	// total FRP 19 -> 34 MW: WASPADA becomes SIAGA on the same event
	{"update", `{"offset":7,"key":"N20|2026-11-12|243|-2.51230|113.80440","changes":{"bright_ti4":362.45,"frp":20}}`},
	// brightness only: no change to the fire
	{"update", `{"offset":8,"key":"N21|2026-11-12|245|-2.51500|113.80600","changes":{"bright_ti5":275.59}}`},
	// unknown detection: ignored
	{"update", `{"offset":9,"key":"N20|2026-11-12|1|-5.00000|110.00000","changes":{"frp":9}}`},
	{"retract", `{"offset":10,"reason":"false_positive","key":"N20|2026-11-12|300|-1.00000|112.00000"}`},
	// 26 hours before the newest detection: beyond the allowed lateness, kept and counted
	{"detection", `{"offset":11,"detection":{"satellite":"N21","acq_date":"2026-11-11","acq_time":100,"latitude":-1.00100,"longitude":112.00100,"confidence":"h","frp":3}}`},
	// unreadable: kept in satellite_rejects
	{"detection", `{"offset":12,"detection":{"satellite":"N20","acq_date":"2026-11-12","acq_time":9999,"latitude":0,"longitude":0,"confidence":"h","frp":1}}`},
}

func feedEvents() []controller.FeedEvent {
	events := make([]controller.FeedEvent, len(feedScript))
	for i, e := range feedScript {
		events[i] = controller.FeedEvent{Offset: int64(i), Type: e.kind, Data: []byte(e.data), ReceivedAt: time.Now()}
	}
	return events
}

// droppingFeed serves feedScript like the real feed (offsets from 0, from inclusive)
// and closes every connection after perConnection events.
func droppingFeed(t *testing.T, perConnection int) (*httptest.Server, *atomic.Int32) {
	connections := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/session" {
			_, _ = fmt.Fprintf(w, `{"latest_offset":%d,"virtual_now":"2026-11-12T03:00:00Z","speed":1}`, len(feedScript)-1)
			return
		}
		connections.Add(1)
		next, _ := strconv.Atoi(r.URL.Query().Get("from"))
		w.Header().Set("Content-Type", "text/event-stream")
		for sent := 0; next < len(feedScript) && sent < perConnection; next, sent = next+1, sent+1 {
			e := feedScript[next]
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", next, e.kind, e.data)
		}
		w.(http.Flusher).Flush()
		if next >= len(feedScript) {
			<-r.Context().Done() // caught up: stay open like a live stream
		}
	}))
	t.Cleanup(server.Close)
	return server, connections
}

type fireSummary struct {
	Ref, Severity string
	Attributes    string
}

func satelliteFires(t *testing.T, ctrl *controller.Controller) []fireSummary {
	t.Helper()
	var hazards []model.HazardEvent
	if err := ctrl.DB.Where("source = ?", "SATELIT").Order("source_ref_id").Find(&hazards).Error; err != nil {
		t.Fatal(err)
	}
	out := make([]fireSummary, len(hazards))
	for i, h := range hazards {
		attrs, _ := json.Marshal(h.Attributes)
		out[i] = fireSummary{h.Ref, h.Severity, string(attrs)}
	}
	return out
}

func TestSatelliteConsumerSurvivesDisconnectsWithoutLossOrDuplicates(t *testing.T) {
	ctrl := pollController(t)
	server, connections := droppingFeed(t, 3)
	consumer := newSatelliteConsumer(server.URL, "token", ctrl)
	consumer.flushEvery = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { consumer.Run(ctx); close(done) }()
	last := int64(len(feedScript) - 1)
	deadline := time.Now().Add(40 * time.Second)
	for {
		offset, err := ctrl.SatelliteFeedOffset(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if offset != nil && *offset == last {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer stuck at offset %v", offset)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if connections.Load() < 4 {
		t.Fatalf("expected the feed to be reconnected several times, got %d connections", connections.Load())
	}

	streamed := satelliteFires(t, ctrl)
	if len(streamed) != 1 || streamed[0].Severity != "SIAGA" || !strings.Contains(streamed[0].Attributes, `"detection_count":3`) ||
		!strings.Contains(streamed[0].Attributes, `"satellites":["N20","N21"]`) ||
		!strings.Contains(streamed[0].Attributes, `"N20|2026-11-12|243|-2.51230|113.80440"`) {
		t.Fatalf("unexpected fires %+v", streamed)
	}
	stats, err := ctrl.SatelliteCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Fires != 1 || stats.LateDetections != 1 || stats.LowConfidence != 1 || stats.Retracted != 1 {
		t.Fatalf("unexpected counts %+v", stats)
	}
	status, err := ctrl.SatelliteFeedStatus(context.Background())
	if err != nil || status.Rejected != 1 {
		t.Fatalf("the unreadable detection must be kept as a reject: %+v, %v", status, err)
	}

	// The same events in one uninterrupted batch give exactly the same fires.
	reference := pollController(t)
	session, err := reference.AcquirePollSession(context.Background(), controller.SatelliteSource)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Controller.IngestFeedBatch(context.Background(), feedEvents()); err != nil {
		t.Fatal(err)
	}
	if want := satelliteFires(t, reference); fmt.Sprint(want) != fmt.Sprint(streamed) {
		t.Fatalf("interrupted run differs from uninterrupted run:\n got %+v\nwant %+v", streamed, want)
	}

	// Replaying everything after a crash changes nothing.
	result, err := session.Controller.IngestFeedBatch(context.Background(), feedEvents())
	if err != nil || result.Skipped != len(feedScript) || len(result.Changed) != 0 {
		t.Fatalf("replay must be skipped: %+v, %v", result, err)
	}
}

func TestSatelliteFeedBatchIsFencedAfterTakeover(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	first, err := ctrl.AcquirePollSession(ctx, controller.SatelliteSource)
	if err != nil {
		t.Fatal(err)
	}
	first.Close() // the owner froze; its lock is gone
	second, err := ctrl.AcquirePollSession(ctx, controller.SatelliteSource)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.Controller.IngestFeedBatch(ctx, feedEvents()[:2]); err == nil {
		t.Fatal("a replaced owner must not commit feed events")
	}
	if offset, _ := ctrl.SatelliteFeedOffset(ctx); offset != nil {
		t.Fatalf("fenced batch moved the offset to %d", *offset)
	}
}
