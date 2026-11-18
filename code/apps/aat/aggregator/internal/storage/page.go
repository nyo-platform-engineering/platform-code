package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"aat/aggregator/internal/model"
)

type HazardQuery struct {
	Source, After, Cursor, Order string
	Limit                        int
}

type HazardPage struct {
	Items      []model.HazardEvent `json:"items"`
	NextAfter  string              `json:"next_after"`
	NextCursor *string             `json:"next_cursor"`
	Availability
}

type Pager interface {
	Page(context.Context, HazardQuery) (HazardPage, error)
}

// Cursors describe the global sort position, independent of shard placement.
type globalCursor struct {
	Version int       `json:"v"`
	Kind    string    `json:"kind"`
	Source  string    `json:"source"`
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
	Start   bool      `json:"start,omitempty"`
}

func encodeGlobal(c globalCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeGlobal(raw, kind, source string) (globalCursor, error) {
	c := globalCursor{Version: 2, Kind: kind, Source: source, Start: true}
	if raw == "" {
		return c, nil
	}
	c = globalCursor{}
	if len(raw) > 4096 {
		return c, ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Version != 2 || c.Kind != kind || c.Source != source || (!c.Start && (c.ID == "" || c.At.IsZero())) {
		return c, ErrInvalidCursor
	}
	return c, nil
}
