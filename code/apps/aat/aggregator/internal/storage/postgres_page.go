package storage

import (
	"context"
	"errors"

	"aat/aggregator/internal/model"
)

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
	query := p.db.WithContext(ctx).Order("occurred_at DESC, id DESC").Limit(q.Limit + 1)
	if q.Source != "" {
		query = query.Where("source = ?", q.Source)
	}
	if !cursor.Start {
		query = query.Where("(occurred_at,id) < (?,?)", cursor.At, cursor.ID)
	}
	if err = query.Find(&page.Items).Error; err != nil {
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
