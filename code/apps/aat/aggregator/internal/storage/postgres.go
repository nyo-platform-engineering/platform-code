package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"aat/aggregator/internal/model"
	"gorm.io/gorm"
)

type Postgres struct {
	db       *gorm.DB
	identity Identity
}

var _ Reader = (*Postgres)(nil)
var _ Writer = (*Postgres)(nil)

// NewPostgres accepts either the root connection for reads or an already-open
// transaction for Save. Node identifies the DATABASE's node, not the API pod.
func NewPostgres(db *gorm.DB, identity Identity) *Postgres {
	if identity.Unit == "" {
		identity.Unit = "postgres"
	}
	return &Postgres{db: db, identity: identity}
}

func (p *Postgres) List(ctx context.Context, source, after string, limit int) ([]model.HazardEvent, error) {
	items := []model.HazardEvent{}
	query := p.db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(limit)
	if source != "" {
		query = query.Where("source = ?", source)
	}
	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}
	utc(items)
	return items, nil
}

func utc(items []model.HazardEvent) {
	for i := range items {
		items[i].Occurred = items[i].Occurred.UTC()
		items[i].Ingested = items[i].Ingested.UTC()
	}
}

type eventCursor struct {
	Version  int       `json:"v"`
	Source   string    `json:"source"`
	Since    string    `json:"since"`
	Ingested time.Time `json:"at"`
	ID       string    `json:"id"`
}

func sinceKey(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func decodeCursor(q EventQuery) (eventCursor, error) {
	var cursor eventCursor
	if q.Cursor == "" {
		return cursor, nil
	}
	if len(q.Cursor) > 4096 {
		return cursor, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
	if err != nil {
		return cursor, ErrInvalidCursor
	}
	if json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || cursor.ID == "" || cursor.Ingested.IsZero() || cursor.Source != q.Source || cursor.Since != sinceKey(q.Since) {
		return cursor, ErrInvalidCursor
	}
	return cursor, nil
}

func (p *Postgres) available() Availability { return Availability{UnavailableUnits: []string{}} }
func (p *Postgres) unavailable() Availability {
	return Availability{Partial: true, UnavailableUnits: []string{p.identity.Unit}}
}

func (p *Postgres) Events(ctx context.Context, q EventQuery) (EventPage, error) {
	page := EventPage{Items: []model.HazardEvent{}, Availability: p.available()}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 {
		return page, errors.New("event limit must be between 1 and 1000")
	}
	cursor, err := decodeCursor(q)
	if err != nil {
		return page, err
	}
	query := p.db.WithContext(ctx).Model(&model.HazardEvent{}).Order("ingested_at ASC, id ASC").Limit(q.Limit + 1)
	if q.Source != "" {
		query = query.Where("source = ?", q.Source)
	}
	if !q.Since.IsZero() {
		query = query.Where("ingested_at >= ?", q.Since)
	}
	if q.Cursor != "" {
		query = query.Where("(ingested_at, id) > (?, ?)", cursor.Ingested, cursor.ID)
	}
	if err = query.Find(&page.Items).Error; err != nil {
		page.Items = []model.HazardEvent{}
		page.Availability = p.unavailable()
		return page, nil
	}
	utc(page.Items)
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		last := page.Items[len(page.Items)-1]
		raw, err := json.Marshal(eventCursor{1, q.Source, sinceKey(q.Since), last.Ingested, last.ID})
		if err != nil {
			return page, err
		}
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
	}
	return page, nil
}

func (p *Postgres) Checksum(ctx context.Context, source string) (Checksum, error) {
	result := Checksum{Source: source, Algorithm: "sha256", AsOf: time.Now().UTC(), Availability: p.available()}
	query := p.db.WithContext(ctx).Model(&model.HazardEvent{}).Select("source_ref_id").Order(`source_ref_id COLLATE "C" ASC`)
	if source != "" {
		query = query.Where("source = ?", source)
	}
	rows, err := query.Rows()
	if err != nil {
		result.Availability = p.unavailable()
		return result, nil
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var ref string
		if err = rows.Scan(&ref); err != nil {
			return Checksum{}, err
		}
		if result.Count > 0 {
			_, _ = io.WriteString(hash, "\n")
		}
		_, _ = io.WriteString(hash, ref)
		result.Count++
	}
	if rows.Err() != nil {
		result.Count = 0
		result.Availability = p.unavailable()
		return result, nil
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	result.Checksum = &checksum
	return result, nil
}

func (p *Postgres) Locations(ctx context.Context, id string) (Locations, error) {
	result := Locations{HazardID: id, Locations: []Location{}}
	var hazard model.HazardEvent
	err := p.db.WithContext(ctx).Select("id").Take(&hazard, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, ErrNotFound
	}
	if err != nil {
		result.Partial = true
		return result, nil
	}
	if p.identity.Node == "" {
		return result, ErrUnknownNode
	}
	result.Locations = []Location{{Node: p.identity.Node, Unit: p.identity.Unit, Role: "primary"}}
	return result, nil
}
