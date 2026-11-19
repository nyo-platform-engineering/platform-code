package storage

import (
	"context"
	"errors"

	"aat/aggregator/internal/model"
)

func (p *Postgres) Revision(ctx context.Context) (int64, error) {
	var revision int64
	err := p.db.WithContext(ctx).Raw("SELECT revision FROM hazard_read_revision WHERE id = 1").Row().Scan(&revision)
	return revision, err
}

func (p *Postgres) Page(ctx context.Context, q HazardQuery) (HazardPage, error) {
	page := HazardPage{Items: []model.HazardEvent{}, Availability: p.available()}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 {
		return page, errors.New("invalid page limit")
	}
	if q.Order == "" || q.Order == "id" {
		if q.Cursor != "" {
			return page, ErrInvalidCursor
		}
		items, err := p.List(ctx, q.Source, q.After, q.Limit)
		if err != nil {
			page.Availability = p.unavailable()
			page.NextAfter = q.After
			return page, nil
		}
		page.Items = items
		if len(items) == q.Limit {
			page.NextAfter = items[len(items)-1].ID
		}
		return page, nil
	}
	if q.Order != "recent" || q.After != "" {
		return page, ErrInvalidCursor
	}
	cursor, err := decodeGlobal(q.Cursor, "recent", q.Source)
	if err != nil {
		return page, err
	}
	// Transfer narrow sort keys from each shard, then fetch full JSON only for
	// the winning rows. One statement preserves the page's snapshot semantics.
	err = p.db.WithContext(ctx).Raw(recentPageSQL, q.Source, q.Source,
		cursor.Start, cursor.At, cursor.ID, q.Limit+1).Scan(&page.Items).Error
	if err != nil {
		page.Items = []model.HazardEvent{}
		page.Availability = p.unavailable()
		retry := encodeGlobal(cursor)
		page.NextCursor = &retry
		return page, nil
	}
	utc(page.Items)
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		last := page.Items[len(page.Items)-1]
		next := encodeGlobal(globalCursor{Version: 2, Kind: "recent", Source: q.Source, At: last.Occurred, ID: last.ID})
		page.NextCursor = &next
	}
	return page, nil
}
