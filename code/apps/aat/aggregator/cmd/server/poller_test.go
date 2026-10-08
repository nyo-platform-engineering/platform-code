package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Fetching uses each source's credentials and preserves the inclusive cursor.
func TestSourcePollerFetchCredentialsAndCursor(t *testing.T) {
	since := time.Date(2026, 9, 26, 0, 0, 0, 123, time.UTC)
	for _, source := range []struct{ name, header, credential, path string }{
		{"BMKG", "X-BMKG-Key", "key", "/seismic-events"},
		{"PVMBG", "Authorization", "Bearer token", "/volcanic-reports"},
	} {
		t.Run(source.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != source.path || r.Header.Get(source.header) != source.credential || r.Header.Get("X-Correlation-ID") != "correlation" {
					t.Error("wrong upstream path, credentials, or correlation ID")
				}
				if r.URL.Query().Get("since") != since.Format(time.RFC3339Nano) {
					t.Error("cursor changed")
				}
				_, _ = w.Write([]byte(`[{"source_id":"1"}]`))
			}))
			defer upstream.Close()
			poller := newSourcePoller(source.name, upstream.URL, source.header, source.credential, nil, nil, upstream.Client())
			records, err := poller.fetch(context.Background(), source.path, &since, "correlation")
			if err != nil || len(records) != 1 {
				t.Fatalf("fetch: %v, %v", records, err)
			}
		})
	}
}

// Full polls use the real controller against an isolated PostgreSQL schema.
func TestSourcePollerStoresAllBMKGResponsesAndMarksSuccess(t *testing.T) {
	ctrl := pollController(t)
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("X-BMKG-Key") != "key" || r.Header.Get("X-Correlation-ID") == "" {
			t.Error("missing upstream credentials or correlation ID")
		}
		if r.URL.Query().Has("since") {
			t.Error("initial poll must import all historical seed records")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/seismic-events" {
			_, _ = w.Write([]byte(`[{"event_id":"eq-1","magnitude":7,"depth_km":10,"epicenter_lat":-8,"epicenter_lon":110,"region_name":"Jawa","occurred_at":"2026-09-25T00:00:00Z","potential_tsunami":true}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"warning_id":"warn-1","related_event_id":"eq-1","threat_level":"Awas","affected_zones":["Jawa"],"estimated_arrival":"2026-09-25T00:30:00Z"}]`))
	}))
	defer upstream.Close()
	poller := newSourcePoller("BMKG", upstream.URL, "X-BMKG-Key", "key", []pollEndpoint{{"/seismic-events", "seismic_events"}, {"/tsunami-warnings", "tsunami_warnings"}}, ctrl, upstream.Client())
	poller.Poll(context.Background())
	state, err := ctrl.GetPollState(context.Background(), "BMKG")
	if err != nil || requests != 2 || !state.Healthy || state.LastSuccessCallerTime == nil {
		t.Fatalf("requests=%d state=%+v error=%v", requests, state, err)
	}
	hazards, err := ctrl.List(context.Background(), "BMKG", "", 100)
	if err != nil || len(hazards) != 1 || hazards[0].Severity != "AWAS" {
		t.Fatalf("hazards=%+v error=%v", hazards, err)
	}
	var warnings []model.Record
	if err := json.Unmarshal(hazards[0].Attributes["tsunami_warnings"], &warnings); err != nil || len(warnings) != 1 {
		t.Fatalf("warnings=%+v error=%v", warnings, err)
	}
}

// If BMKG's second endpoint fails, neither the first response nor a cursor is saved.
func TestSourcePollerMarksFailureWithoutIngestingPartialResponse(t *testing.T) {
	ctrl := pollController(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/seismic-events" {
			_, _ = w.Write([]byte(`[{"event_id":"eq-1"}]`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	poller := newSourcePoller("BMKG", upstream.URL, "X-BMKG-Key", "key", []pollEndpoint{{"/seismic-events", "seismic_events"}, {"/tsunami-warnings", "tsunami_warnings"}}, ctrl, upstream.Client())
	poller.Poll(context.Background())
	state, err := ctrl.GetPollState(context.Background(), "BMKG")
	if err != nil || state.Healthy || state.LastError == nil {
		t.Fatalf("state=%+v error=%v", state, err)
	}
	var savedState model.PollState
	if err := ctrl.DB.Take(&savedState, "source = ?", "BMKG").Error; err != nil || savedState.LastSuccessCallerTime != nil {
		t.Fatalf("failed poll saved a cursor: %+v, %v", savedState, err)
	}
	var rawEvents int64
	if err := ctrl.DB.Model(&model.SeismicEvent{}).Count(&rawEvents).Error; err != nil || rawEvents != 0 {
		t.Fatalf("partial data persisted: %d, %v", rawEvents, err)
	}
}

func pollController(t *testing.T) *controller.Controller {
	t.Helper()
	url := os.Getenv("AAT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AAT_TEST_DATABASE_URL to run full polling integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("aat_poll_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return &controller.Controller{DB: db}
}
