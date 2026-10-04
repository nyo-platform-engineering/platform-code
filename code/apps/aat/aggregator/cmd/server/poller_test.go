package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aat/aggregator/internal/aggregate"
)

type memoryPollStore struct {
	mu       sync.Mutex
	state    aggregate.PollState
	body     map[string][]aggregate.Record
	failures []string
}

func (s *memoryPollStore) PollState(context.Context, string) (aggregate.PollState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *memoryPollStore) Ingest(_ context.Context, _ string, body map[string][]aggregate.Record) ([]aggregate.Hazard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = body
	return []aggregate.Hazard{{ID: "hazard-1"}}, nil
}

func (s *memoryPollStore) MarkPollSuccess(_ context.Context, source string, cursor time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = aggregate.PollState{Source: source, Healthy: true, Cursor: &cursor}
	return nil
}

func (s *memoryPollStore) MarkPollFailure(_ context.Context, _ string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, message)
	return nil
}

func TestSourcePollerStoresAllBMKGResponsesAndMarksSuccess(t *testing.T) {
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("X-BMKG-Key") != "key" || r.Header.Get("X-Correlation-ID") == "" {
			t.Fatal("missing upstream credentials or correlation ID")
		}
		if r.URL.Query().Has("since") {
			t.Fatal("initial poll unexpectedly used a cursor")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/seismic-events" {
			_, _ = w.Write([]byte(`[{"event_id":"eq-1"}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"warning_id":"warn-1"}]`))
	}))
	defer upstream.Close()
	store := &memoryPollStore{}
	poller := newSourcePoller("BMKG", upstream.URL, "X-BMKG-Key", "key", []pollEndpoint{{"/seismic-events", "seismic_events"}, {"/tsunami-warnings", "tsunami_warnings"}}, store, upstream.Client())
	poller.Poll(context.Background())
	if requests != 2 || len(store.body["seismic_events"]) != 1 || len(store.body["tsunami_warnings"]) != 1 || !store.state.Healthy {
		t.Fatalf("requests=%d body=%v state=%+v", requests, store.body, store.state)
	}
}

func TestSourcePollerMarksFailureWithoutIngestingPartialResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	store := &memoryPollStore{}
	poller := newSourcePoller("PVMBG", upstream.URL, "Authorization", "Bearer token", []pollEndpoint{{"/volcanic-reports", "volcanic_reports"}}, store, upstream.Client())
	poller.Poll(context.Background())
	if len(store.failures) != 1 || store.body != nil {
		t.Fatalf("failures=%v body=%v", store.failures, store.body)
	}
}
