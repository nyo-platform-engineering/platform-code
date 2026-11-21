package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// clustering is one satellite clustering implementation: the batched one in use or
// the per-statement reference it replaced.
type clustering func(ctrl *Controller, tx *gorm.DB, records []model.Record) ([]model.HazardEvent, error)

var (
	batched   clustering = (*Controller).satelliteHazards
	reference clustering = (*Controller).satelliteHazardsReference
)

// queryCounter counts the statements a connection pool sends.
type queryCounter struct{ n atomic.Int64 }

func (q *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.n.Add(1)
	return ctx
}
func (q *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func satelliteSchema(t testing.TB, name string) (*Controller, *queryCounter) {
	t.Helper()
	url := os.Getenv("AAT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AAT_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("aat_sat_%s_%d", name, time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	counter := &queryCounter{}
	config.ConnConfig.Tracer = counter
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
	return &Controller{DB: db}, counter
}

// applySatellite runs one batch the way IngestFeedBatch does: parse, cluster, store.
func applySatellite(t testing.TB, ctrl *Controller, impl clustering, events []FeedEvent) []model.HazardEvent {
	t.Helper()
	records := []model.Record{}
	for _, event := range events {
		record, reason := feedRecord(event)
		if reason == nil {
			if _, reason = mapper.Satellite(record); reason == nil {
				records = append(records, record)
			}
		}
	}
	var changed []model.HazardEvent
	err := ctrl.DB.Transaction(func(tx *gorm.DB) error {
		hazards, err := impl(ctrl, tx, records)
		if err != nil {
			return err
		}
		changed, err = storage.NewPostgres(tx, storage.Identity{}).Save(context.Background(), hazards)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

// syntheticFeed is a seeded feed shaped like the practice one: satellite passes over
// hotspots that two satellites see a few hundred metres apart, fires that grow and
// touch (merges), isolated roofs, low-confidence points, re-sent detections, updates
// (some making a detection ineligible: splits; some making one eligible again),
// retractions, late detections and updates for unknown keys.
func syntheticFeed(seed int64, n int, base time.Time) []FeedEvent {
	r := rand.New(rand.NewSource(seed))
	type spot struct{ lat, lon float64 }
	spots := make([]spot, 40)
	for i := range spots {
		spots[i] = spot{-3 + 5*r.Float64(), 100 + 17*r.Float64()}
		if i%4 == 1 { // a neighbour 1-2 km away: their fires touch and merge
			spots[i] = spot{spots[i-1].lat + 0.012*r.Float64(), spots[i-1].lon + 0.012*r.Float64()}
		}
	}
	confidences := []string{"h", "n", "n", "n", "l"}
	var keys []string
	seen := map[string]bool{}
	events := make([]FeedEvent, 0, n)
	clock := base
	add := func(kind, data string) {
		events = append(events, FeedEvent{Offset: int64(len(events)), Type: kind, Data: []byte(data), ReceivedAt: time.Now()})
	}
	detection := func(at time.Time, lat, lon float64) {
		satellite := []string{"N20", "N21"}[r.Intn(2)]
		hhmm := at.Hour()*100 + at.Minute()
		key := fmt.Sprintf("%s|%s|%d|%.5f|%.5f", satellite, at.Format("2006-01-02"), hhmm, lat, lon)
		if seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
		add("detection", fmt.Sprintf(`{"detection":{"satellite":%q,"acq_date":%q,"acq_time":%d,"latitude":%.5f,"longitude":%.5f,"confidence":%q,"frp":%.1f}}`,
			satellite, at.Format("2006-01-02"), hhmm, lat, lon, confidences[r.Intn(len(confidences))], 1+40*r.Float64()))
	}
	for len(events) < n {
		clock = clock.Add(time.Duration(20+r.Intn(100)) * time.Minute)
		active := spots[:10+r.Intn(len(spots)-10)]
		for _, s := range active {
			for range r.Intn(5) {
				detection(clock.Add(time.Duration(r.Intn(15))*time.Minute), s.lat+0.008*(r.Float64()-0.5), s.lon+0.008*(r.Float64()-0.5))
			}
		}
		for range 3 + r.Intn(10) { // roofs
			detection(clock, -4+7*r.Float64(), 98+20*r.Float64())
		}
		for range 2 + r.Intn(6) {
			switch p := r.Float64(); {
			case len(keys) == 0:
			case p < 0.35: // update confidence (may switch eligibility) or FRP
				key := keys[r.Intn(len(keys))]
				if r.Intn(2) == 0 {
					add("update", fmt.Sprintf(`{"key":%q,"changes":{"confidence":%q}}`, key, confidences[r.Intn(len(confidences))]))
				} else {
					add("update", fmt.Sprintf(`{"key":%q,"changes":{"frp":%.1f}}`, key, 1+120*r.Float64()))
				}
			case p < 0.55:
				add("retract", fmt.Sprintf(`{"reason":"false_positive","key":%q}`, keys[r.Intn(len(keys))]))
			case p < 0.75: // re-sent unchanged (same key: an already-stored detection)
				key := keys[r.Intn(len(keys))]
				parts := strings.Split(key, "|")
				add("detection", fmt.Sprintf(`{"detection":{"satellite":%q,"acq_date":%q,"acq_time":%s,"latitude":%s,"longitude":%s,"confidence":"n","frp":5}}`,
					parts[0], parts[1], parts[2], parts[3], parts[4]))
			case p < 0.9: // late: beyond the 6 h lateness
				s := spots[r.Intn(len(spots))]
				detection(clock.Add(-time.Duration(7+r.Intn(30))*time.Hour), s.lat+0.004*(r.Float64()-0.5), s.lon+0.004*(r.Float64()-0.5))
			default:
				add("update", `{"key":"N20|2020-01-01|100|-1.00000|100.00000","changes":{"frp":9}}`)
			}
		}
	}
	return events[:n]
}

// satelliteState is everything the clustering leaves behind, minus arrival times.
func satelliteState(t testing.TB, ctrl *Controller) string {
	t.Helper()
	var out strings.Builder
	var rows []model.SatelliteDetection
	if err := ctrl.DB.Order("detection_key").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		ref := "-"
		if r.FireRef != nil {
			ref = *r.FireRef
		}
		fmt.Fprintf(&out, "D %s %s %s %v %v %s %v late=%v retracted=%v fire=%s\n", r.Key, r.Satellite,
			r.DetectedAt.UTC().Format(time.RFC3339), r.Latitude, r.Longitude, r.Confidence, r.FRP, r.Late, r.Retracted, ref)
	}
	var fires []model.SatelliteFire
	if err := ctrl.DB.Order("ref").Find(&fires).Error; err != nil {
		t.Fatal(err)
	}
	for _, f := range fires {
		merged := "-"
		if f.MergedInto != nil {
			merged = *f.MergedInto
		}
		fmt.Fprintf(&out, "F %s anchor=%s formed=%v merged=%s\n", f.Ref, f.AnchorKey, f.Formed, merged)
	}
	var hazards []model.HazardEvent
	if err := ctrl.DB.Where("source = ?", SatelliteSource).Order("source_ref_id").Find(&hazards).Error; err != nil {
		t.Fatal(err)
	}
	for _, h := range hazards {
		fmt.Fprintf(&out, "H %s %s %s %s %s %v %v %s %s\n", h.ID, h.Ref, h.Type, h.Severity, h.Area, h.Latitude, h.Longitude,
			h.Occurred.UTC().Format(time.RFC3339), canonical(t, h.Attributes))
	}
	return out.String()
}

func canonical(t testing.TB, attributes model.Record) string {
	raw, err := json.Marshal(attributes)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func changedSummary(t testing.TB, changed []model.HazardEvent) string {
	lines := make([]string, len(changed))
	for i, h := range changed {
		lines[i] = h.Ref + " " + h.Severity + " " + canonical(t, h.Attributes)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// The reference applied one event per batch is the ground truth: it is how the
// per-statement code behaves when nothing in a batch can go stale. (In larger batches
// it can: it kept an in-memory copy of each batch row that merges and splits did not
// update, and saving that copy later in the batch wrote the old fire back, so its
// result depended on the batch size. The batched code keeps its copies in step.)
//
// The batched clustering must leave exactly those tables for every batch size, and
// report exactly the same changed events event by event.
func TestSatelliteBatchedClusteringMatchesReference(t *testing.T) {
	base := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for _, seed := range []int64{1, 2, 3} {
		events := syntheticFeed(seed, 3000, base)
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			ref, _ := satelliteSchema(t, "ref")
			one, _ := satelliteSchema(t, "one")
			for i, event := range events {
				want := changedSummary(t, applySatellite(t, ref, reference, []FeedEvent{event}))
				have := changedSummary(t, applySatellite(t, one, batched, []FeedEvent{event}))
				if want != have {
					t.Fatalf("event %d (%s %s): changed events differ\nreference:\n%s\nbatched:\n%s", i, event.Type, event.Data, want, have)
				}
			}
			want := satelliteState(t, ref)
			if !strings.Contains(want, "merged=fire-") || !strings.Contains(want, "retracted=true") || !strings.Contains(want, "late=true") {
				t.Fatal("the synthetic feed must exercise merges, retractions and late detections")
			}
			compareState(t, "batch 1", want, satelliteState(t, one))
			for _, size := range []int{37, 200, 1000} {
				ctrl, _ := satelliteSchema(t, fmt.Sprintf("b%d", size))
				for start := 0; start < len(events); start += size {
					applySatellite(t, ctrl, batched, events[start:min(start+size, len(events))])
				}
				compareState(t, fmt.Sprintf("batch %d", size), want, satelliteState(t, ctrl))
			}
		})
	}
}

func compareState(t *testing.T, label, want, have string) {
	t.Helper()
	if want == have {
		return
	}
	wl, hl := strings.Split(want, "\n"), strings.Split(have, "\n")
	for i := range min(len(wl), len(hl)) {
		if wl[i] != hl[i] {
			t.Fatalf("%s: tables differ at line %d:\nreference: %s\nbatched:   %s", label, i, wl[i], hl[i])
		}
	}
	t.Fatalf("%s: tables differ in length: %d vs %d lines", label, len(wl), len(hl))
}

// Throughput of both implementations over a realistically sized table (set
// AAT_SATELLITE_BENCH=1). The background rows give the neighbour queries the index
// work they do on a lab that has run for days.
func TestSatelliteClusteringThroughput(t *testing.T) {
	if os.Getenv("AAT_SATELLITE_BENCH") == "" {
		t.Skip("set AAT_SATELLITE_BENCH=1 (and AAT_TEST_DATABASE_URL) to measure throughput")
	}
	background := 1_000_000
	base := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	events := syntheticFeed(7, 6000, base.Add(90*24*time.Hour))
	for _, impl := range []struct {
		name string
		fn   clustering
	}{{"reference", reference}, {"batched", batched}} {
		ctrl, counter := satelliteSchema(t, impl.name)
		// ~90 days of history across the same region, in fires of ~40 detections.
		if err := ctrl.DB.Exec(fmt.Sprintf(`INSERT INTO satellite_detections (detection_key, satellite, detected_at, latitude, longitude,
			confidence, frp_mw, late, retracted, fire_ref, received_at)
			SELECT 'BG|' || i, 'N20', timestamptz '2026-11-12' + (i * interval '7.776 seconds'),
			       -3 + 5 * random(), 100 + 17 * random(), 'n', 5, false, false,
			       CASE WHEN i %% 3 = 0 THEN NULL ELSE 'fire-bg' || (i / 40) END, now()
			FROM generate_series(1, %d) AS i`, background)).Error; err != nil {
			t.Fatal(err)
		}
		if err := ctrl.DB.Exec("ANALYZE satellite_detections").Error; err != nil {
			t.Fatal(err)
		}
		// Warm up, then measure.
		applySatellite(t, ctrl, impl.fn, events[:400])
		counter.n.Store(0)
		started := time.Now()
		measured := 0
		for start := 400; start < len(events); start += 200 {
			batch := events[start:min(start+200, len(events))]
			applySatellite(t, ctrl, impl.fn, batch)
			measured += len(batch)
		}
		elapsed := time.Since(started)
		t.Logf("%-9s %6.0f events/s  (%d events in %v, %.1f statements per event)", impl.name,
			float64(measured)/elapsed.Seconds(), measured, elapsed.Round(time.Millisecond), float64(counter.n.Load())/float64(measured))
	}
}
