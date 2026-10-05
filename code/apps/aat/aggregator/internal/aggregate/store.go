package aggregate

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct {
	Pool *pgxpool.Pool
}

type PollState struct {
	Source      string
	Healthy     bool
	LastSuccess *time.Time
	Cursor      *time.Time
}

type SourceStatus struct {
	Source      string     `json:"source"`
	Healthy     bool       `json:"healthy"`
	LastSuccess *time.Time `json:"last_success_at"`
}

type OutboxMessage struct {
	ID       int64
	EventKey string
	Payload  []byte
}

type Invalid struct {
	Err error
}

func (e Invalid) Error() string {
	return e.Err.Error()
}

// Initialize once in deployment; transaction-scoped advisory lock also permits concurrent startup.
func (s Store) Init(ctx context.Context) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}

	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(403100)"); e != nil {
		return e
	}

	_, e = tx.Exec(ctx, schema)
	if e != nil {
		return e
	}

	return tx.Commit(ctx)
}

func (s Store) Ingest(ctx context.Context, source string, body map[string][]Record) ([]Hazard, error) {
	if len(body) == 0 {
		return nil, Invalid{fmt.Errorf("empty batch")}
	}

	for k := range body {
		if source == "BMKG" && (k == "seismic_events" || k == "tsunami_warnings") {
			continue
		}

		if source == "PVMBG" && k == "volcanic_reports" {
			continue
		}

		return nil, Invalid{fmt.Errorf("unexpected batch key %s", k)}
	}

	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}

	defer tx.Rollback(ctx)
	// Lock only each affected source event, across replicas. Sorted acquisition prevents deadlocks.
	keys := map[string]bool{}
	for _, r := range body["seismic_events"] {
		h, e := Seismic(r, nil)
		if e != nil {
			return nil, Invalid{e}
		}

		keys[h.Ref] = true
	}

	for _, w := range body["tsunami_warnings"] {
		_, ref, e := Warning(w)
		if e != nil {
			return nil, Invalid{e}
		}

		keys[ref] = true
	}

	events := []Hazard{}
	for _, r := range body["volcanic_reports"] {
		h, e := Volcanic(r)
		if e != nil {
			return nil, Invalid{e}
		}

		keys[h.Ref] = true
		events = append(events, h)
	}

	sorted := []string{}
	for k := range keys {
		sorted = append(sorted, k)
	}

	sort.Strings(sorted)
	for _, k := range sorted {
		if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", source+":"+k); e != nil {
			return nil, e
		}
	}

	for _, r := range body["seismic_events"] {
		id, _ := str(r, "event_id")
		b, _ := json.Marshal(r)
		if _, e = tx.Exec(ctx, "INSERT INTO raw_seismic VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET document=excluded.document", id, b); e != nil {
			return nil, e
		}
	}

	for _, w := range body["tsunami_warnings"] {
		id, ref, _ := Warning(w)
		b, _ := json.Marshal(w)
		tag, e := tx.Exec(
			ctx,
			`INSERT INTO tsunami_warnings VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET document=excluded.document WHERE tsunami_warnings.event_id=excluded.event_id`,
			id,
			ref,
			b,
		)
		if e != nil {
			return nil, e
		}

		if tag.RowsAffected() == 0 {
			return nil, Invalid{fmt.Errorf("warning related_event_id is immutable")}
		}
	}

	if source == "BMKG" {
		for _, ref := range sorted {
			var raw []byte
			e = tx.QueryRow(ctx, "SELECT document FROM raw_seismic WHERE id=$1", ref).Scan(&raw)
			if errors.Is(e, pgx.ErrNoRows) {
				continue
			}

			if e != nil {
				return nil, e
			}

			var r Record
			if e = json.Unmarshal(raw, &r); e != nil {
				return nil, e
			}

			rows, e := tx.Query(ctx, "SELECT document FROM tsunami_warnings WHERE event_id=$1 ORDER BY id", ref)
			if e != nil {
				return nil, e
			}

			ws := []Record{}
			for rows.Next() {
				var b []byte
				if e = rows.Scan(&b); e != nil {
					rows.Close()
					return nil, e
				}

				var w Record
				if e = json.Unmarshal(b, &w); e != nil {
					rows.Close()
					return nil, e
				}

				ws = append(ws, w)
			}

			rows.Close()
			if e = rows.Err(); e != nil {
				return nil, e
			}

			h, e := Seismic(r, ws)
			if e != nil {
				return nil, Invalid{e}
			}

			events = append(events, h)
		}
	}

	changed := []Hazard{}
	for _, h := range events {
		attributes, e := json.Marshal(h.Attributes)
		if e != nil {
			return nil, e
		}
		tag, e := tx.Exec(ctx, upsertHazard,
			h.ID, h.Source, h.Ref, h.Type, h.Severity, h.Area,
			h.Latitude, h.Longitude, h.Occurred, h.Ingested, attributes,
		)
		if e != nil {
			return nil, e
		}

		if tag.RowsAffected() > 0 {
			payload, e := json.Marshal(h)
			if e != nil {
				return nil, e
			}
			eventKey := h.ID + "-" + h.Ingested.UTC().Format(time.RFC3339Nano)
			if _, e = tx.Exec(ctx, `INSERT INTO hazard_outbox(event_key, payload) VALUES($1, $2) ON CONFLICT(event_key) DO NOTHING`, eventKey, payload); e != nil {
				return nil, e
			}
			changed = append(changed, h)
		}
	}

	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}

	return changed, nil
}

func (s Store) List(ctx context.Context, source, after string, limit int) ([]Hazard, error) {
	rows, e := s.Pool.Query(ctx, `SELECT id, source, source_ref_id, hazard_type, severity, area_name,
        latitude, longitude, occurred_at, ingested_at, attributes
        FROM hazards WHERE ($1='' OR source=$1) AND id>$2 ORDER BY id LIMIT $3`, source, after, limit)
	if e != nil {
		return nil, e
	}

	defer rows.Close()
	items := []Hazard{}
	for rows.Next() {
		var h Hazard
		if e = rows.Scan(
			&h.ID, &h.Source, &h.Ref, &h.Type, &h.Severity, &h.Area,
			&h.Latitude, &h.Longitude, &h.Occurred, &h.Ingested, &h.Attributes,
		); e != nil {
			return nil, e
		}
		h.Occurred = h.Occurred.UTC()
		h.Ingested = h.Ingested.UTC()

		items = append(items, h)
	}

	return items, rows.Err()
}

func (s Store) PollState(ctx context.Context, source string) (PollState, error) {
	state := PollState{Source: source}
	var lastSuccess, cursor *time.Time
	e := s.Pool.QueryRow(ctx, `SELECT healthy, last_success_at, cursor_at FROM source_polls WHERE source=$1`, source).Scan(&state.Healthy, &lastSuccess, &cursor)
	if errors.Is(e, pgx.ErrNoRows) {
		return state, nil
	}
	if e != nil {
		return PollState{}, e
	}
	state.LastSuccess = utc(lastSuccess)
	state.Cursor = utc(cursor)
	return state, nil
}

func (s Store) MarkPollSuccess(ctx context.Context, source string, cursor time.Time) error {
	_, e := s.Pool.Exec(ctx, `INSERT INTO source_polls(source, healthy, last_success_at, cursor_at, last_error_at, last_error)
        VALUES($1, true, now(), $2, NULL, NULL)
        ON CONFLICT(source) DO UPDATE SET healthy=true, last_success_at=excluded.last_success_at,
        cursor_at=excluded.cursor_at, last_error_at=NULL, last_error=NULL`, source, cursor.UTC())
	return e
}

func (s Store) MarkPollFailure(ctx context.Context, source, message string) error {
	_, e := s.Pool.Exec(ctx, `INSERT INTO source_polls(source, healthy, last_error_at, last_error)
        VALUES($1, false, now(), $2)
        ON CONFLICT(source) DO UPDATE SET healthy=false, last_error_at=excluded.last_error_at, last_error=excluded.last_error`, source, message)
	return e
}

func (s Store) SourceStatuses(ctx context.Context) ([]SourceStatus, error) {
	rows, e := s.Pool.Query(ctx, `SELECT sources.source, COALESCE(p.healthy, false), p.last_success_at
        FROM (VALUES ('BMKG'::text), ('PVMBG'::text)) AS sources(source)
        LEFT JOIN source_polls p USING(source) ORDER BY sources.source`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	statuses := []SourceStatus{}
	for rows.Next() {
		var status SourceStatus
		if e = rows.Scan(&status.Source, &status.Healthy, &status.LastSuccess); e != nil {
			return nil, e
		}
		status.LastSuccess = utc(status.LastSuccess)
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func utc(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}

func (s Store) ClaimOutbox(ctx context.Context, limit int, lease time.Duration) ([]OutboxMessage, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `WITH selected AS (
        SELECT id FROM hazard_outbox
        WHERE published_at IS NULL AND (lease_until IS NULL OR lease_until < now())
        ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED
    )
    UPDATE hazard_outbox AS outbox
    SET lease_until = now() + $2, attempts = attempts + 1
    FROM selected WHERE outbox.id = selected.id
    RETURNING outbox.id, outbox.event_key, outbox.payload`, limit, lease)
	if e != nil {
		return nil, e
	}
	items := []OutboxMessage{}
	for rows.Next() {
		var item OutboxMessage
		if e = rows.Scan(&item.ID, &item.EventKey, &item.Payload); e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, item)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return items, nil
}

func (s Store) MarkOutboxPublished(ctx context.Context, id int64) error {
	_, e := s.Pool.Exec(ctx, `UPDATE hazard_outbox SET published_at=now(), lease_until=NULL WHERE id=$1 AND published_at IS NULL`, id)
	return e
}
