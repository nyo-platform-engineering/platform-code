package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
)

const m2VolcanicReport = `{"report_id":"m2-v1","volcano_id":"MERAPI","alert_level":"Siaga","eruption_count_24h":2,"ash_column_height_m":100,"reported_at":"2026-11-18T00:00:00Z"}`

func m2Batch(t *testing.T) map[string][]model.Record {
	t.Helper()
	var record model.Record
	if err := json.Unmarshal([]byte(m2VolcanicReport), &record); err != nil {
		t.Fatal(err)
	}
	return map[string][]model.Record{"volcanic_reports": {record}}
}

func TestM2PollOwnershipIsExclusiveAndIndependentPerSource(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	first, err := ctrl.AcquirePollSession(ctx, "BMKG")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := ctrl.AcquirePollSession(ctx, "BMKG"); !errors.Is(err, controller.ErrPollBusy) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second owner must be rejected: %v", err)
	}
	other, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatalf("independent source blocked: %v", err)
	}
	other.Close()
	first.Close()
	next, err := ctrl.AcquirePollSession(ctx, "BMKG")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.Fence.Epoch <= first.Fence.Epoch {
		t.Fatal("takeover did not advance epoch")
	}
	if err := first.Controller.ValidatePollOwner(ctx, "BMKG"); err == nil {
		t.Fatal("closed owner reconnected")
	}
}

func TestM2TerminatedOwnerCannotCommitAfterTakeover(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	old, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := ctrl.DB.Exec("SELECT pg_terminate_backend(?::integer)", old.Fence.BackendPID).Error; err != nil {
		t.Fatal(err)
	}
	next, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	batch := m2Batch(t)
	cursor, err := sourceCursor("PVMBG", batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Controller.IngestPollBatch(ctx, "PVMBG", batch, cursor); err == nil {
		t.Fatal("stale session wrote after takeover")
	}
	if err := old.Controller.MarkPollFailure(ctx, "PVMBG", time.Now(), "stale error"); err == nil {
		t.Fatal("stale session changed source status")
	}
	changed, err := next.Controller.IngestPollBatch(ctx, "PVMBG", batch, cursor)
	if err != nil || len(changed) != 1 {
		t.Fatalf("new owner failed: %v, %v", changed, err)
	}
	if next.Fence.Epoch <= old.Fence.Epoch {
		t.Fatal("epoch did not increase")
	}
	changed, err = next.Controller.IngestPollBatch(ctx, "PVMBG", batch, cursor)
	if err != nil || len(changed) != 0 {
		t.Fatalf("retry duplicated event: %v, %v", changed, err)
	}
}

func TestM2ServerExpiresIdleOwnerWithoutClientCooperation(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	old, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	// Shorten the timeout and leave the owner idle without closing it.
	if err := old.Controller.DB.Exec("SET idle_session_timeout = '200ms'").Error; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	var next *controller.PollSession
	for time.Now().Before(deadline) {
		next, err = ctrl.AcquirePollSession(ctx, "PVMBG")
		if err == nil {
			break
		}
		if !errors.Is(err, controller.ErrPollBusy) {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if next == nil {
		t.Fatal("idle owner was not released by PostgreSQL")
	}
	defer next.Close()
	if err := old.Controller.ValidatePollOwner(ctx, "PVMBG"); err == nil {
		t.Fatal("expired owner revived")
	}
	if next.Fence.Epoch <= old.Fence.Epoch {
		t.Fatal("takeover did not fence old epoch")
	}
}

func TestM2DelayedUpstreamResponseIsFencedAfterTakeover(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	old, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Poll-Owner") != old.Fence.Owner || r.Header.Get("X-Poll-Epoch") == "" {
			t.Error("missing upstream ownership evidence")
		}
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("[" + m2VolcanicReport + "]"))
	}))
	defer upstream.Close()
	// Unblock the handler before closing the test server.
	defer unblock.Do(func() { close(release) })
	result := make(chan error, 1)
	go func() {
		p := newSourcePoller("PVMBG", upstream.URL, "Authorization", "Bearer test", []pollEndpoint{{"/volcanic-reports", "volcanic_reports"}}, old.Controller, upstream.Client())
		result <- p.Poll(ctx)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old owner never fetched")
	}
	if err := ctrl.DB.Exec("SELECT pg_terminate_backend(?::integer)", old.Fence.BackendPID).Error; err != nil {
		t.Fatal(err)
	}
	next, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	batch := m2Batch(t)
	cursor, err := sourceCursor("PVMBG", batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.Controller.IngestPollBatch(ctx, "PVMBG", batch, cursor); err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(release) })
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("delayed old response was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old poll did not return")
	}
	state, err := ctrl.GetPollState(ctx, "PVMBG")
	if err != nil || !state.Healthy || state.LastError != nil {
		t.Fatalf("old owner corrupted new status: %+v, %v", state, err)
	}
	var messages int64
	if err := ctrl.DB.Model(&model.HazardOutbox{}).Count(&messages).Error; err != nil || messages != 1 {
		t.Fatalf("duplicate outbox effect: %d, %v", messages, err)
	}
}

func TestM2PollCommitRollsBackHazardsOutboxAndProgressTogether(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	owner, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	batch := m2Batch(t)
	cursor, err := sourceCursor("PVMBG", batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Reject progress after hazards and outbox messages have been written.
	if err := ctrl.DB.Exec("ALTER TABLE poll_states ADD CONSTRAINT reject_poll_success CHECK (NOT healthy)").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Controller.IngestPollBatch(ctx, "PVMBG", batch, cursor); err == nil {
		t.Fatal("expected injected progress failure")
	}
	for _, table := range []string{"hazard_events", "hazard_outboxes", "poll_states"} {
		var count int64
		if err := ctrl.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s survived rollback: count=%d err=%v", table, count, err)
		}
	}
	if err := ctrl.DB.Exec("ALTER TABLE poll_states DROP CONSTRAINT reject_poll_success").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Controller.IngestPollBatch(ctx, "PVMBG", batch, cursor); err != nil {
		t.Fatal(err)
	}
	state, err := ctrl.GetPollState(ctx, "PVMBG")
	if err != nil || state.FetchCursor == nil || !state.FetchCursor.Equal(*cursor) || !state.Healthy {
		t.Fatalf("wrong progress: %+v, %v", state, err)
	}
	// A live connection with an old epoch must also be rejected.
	owner.Controller.PollOwner.Epoch--
	if _, err := owner.Controller.IngestPollBatch(ctx, "PVMBG", batch, cursor); !errors.Is(err, controller.ErrPollFenced) {
		t.Fatalf("wrong epoch accepted: %v", err)
	}
}

func TestM2ThreePollersKeepOneOwnerAndStopOnCancellation(t *testing.T) {
	ctrl := pollController(t)
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("[" + m2VolcanicReport + "]"))
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var group sync.WaitGroup
	for range 3 {
		group.Go(func() {
			p := newSourcePoller("PVMBG", upstream.URL, "Authorization", "Bearer test", []pollEndpoint{{"/volcanic-reports", "volcanic_reports"}}, ctrl, upstream.Client())
			p.Run(ctx, 100*time.Millisecond)
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if requests.Load() < 3 {
		t.Error("polling did not progress")
	}
	var owner model.PollOwner
	if err := ctrl.DB.Take(&owner, "source = ?", "PVMBG").Error; err != nil {
		t.Error(err)
	}
	if owner.Epoch != 1 {
		t.Errorf("followers took ownership while leader was healthy: epoch %d", owner.Epoch)
	}
	cancel()
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pollers did not stop")
	}
	// Socket close is asynchronous at the server, especially over a network.
	next, err := ctrl.AcquirePollSession(context.Background(), "PVMBG")
	deadline = time.Now().Add(time.Second)
	for errors.Is(err, controller.ErrPollBusy) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		next, err = ctrl.AcquirePollSession(context.Background(), "PVMBG")
	}
	if err != nil {
		t.Fatalf("shutdown retained ownership: %v", err)
	}
	next.Close()
}

func TestM2SourceCursorUsesEventTimeAndPreservesInclusiveBoundary(t *testing.T) {
	batch := m2Batch(t)
	stamp, err := sourceCursor("PVMBG", batch, nil)
	if err != nil || stamp == nil || stamp.Format(time.RFC3339) != "2026-11-18T00:00:00Z" {
		t.Fatalf("wrong event watermark: %v, %v", stamp, err)
	}
	unchanged, err := sourceCursor("PVMBG", map[string][]model.Record{}, stamp)
	if err != nil || !unchanged.Equal(*stamp) {
		t.Fatal("empty result advanced cursor")
	}
	var seismic model.Record
	_ = json.Unmarshal([]byte(`{"occurred_at":"2026-11-18T00:00:00Z"}`), &seismic)
	var warning model.Record
	_ = json.Unmarshal([]byte(`{"estimated_arrival":"2099-01-01T00:00:00Z"}`), &warning)
	bmkg, err := sourceCursor("BMKG", map[string][]model.Record{"seismic_events": {seismic}, "tsunami_warnings": {warning}}, nil)
	if err != nil || !bmkg.Equal(*stamp) {
		t.Fatal("future arrival time corrupted cursor")
	}
	batch["volcanic_reports"][0]["reported_at"] = json.RawMessage(`"bad"`)
	if _, err := sourceCursor("PVMBG", batch, stamp); err == nil {
		t.Fatal("invalid cursor time accepted")
	}
}
