// Package storage defines the canonical HazardEvent read/write contracts.
// Source-specific state and outbox delivery remain owned by the Aggregator.
package storage

import (
	"aat/aggregator/internal/model"
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidCursor = errors.New("invalid cursor or cursor filters changed")
	ErrNotFound      = errors.New("hazard not found")
	ErrUnknownNode   = errors.New("storage node identity is not configured")
)

type Identity struct{ Node, Unit string }

type Availability struct {
	Partial          bool     `json:"partial"`
	UnavailableUnits []string `json:"unavailable_units"`
}

type EventQuery struct {
	Source string
	Since  time.Time
	Limit  int
	Cursor string
}

type EventPage struct {
	Items      []model.HazardEvent `json:"items"`
	NextCursor *string             `json:"next_cursor"`
	Availability
}

type Checksum struct {
	Source    string    `json:"source"`
	Count     int64     `json:"count"`
	Checksum  *string   `json:"checksum"`
	Algorithm string    `json:"algorithm"`
	AsOf      time.Time `json:"as_of"`
	Availability
}

type Location struct {
	Node string `json:"node"`
	Unit string `json:"unit"`
	Role string `json:"role"`
}

type Locations struct {
	HazardID  string     `json:"hazard_id"`
	Locations []Location `json:"locations"`
	Partial   bool       `json:"partial"`
}

// Reader preserves the legacy ID-ordered List alongside the audit contract.
// Audit reads report unavailable storage explicitly; an empty complete result
// must never be used to represent an unavailable unit.
type Reader interface {
	List(context.Context, string, string, int) ([]model.HazardEvent, error)
	Events(context.Context, EventQuery) (EventPage, error)
	Checksum(context.Context, string) (Checksum, error)
	Locations(context.Context, string) (Locations, error)
}

// Writer applies idempotent canonical upserts and their outbox entries in the
// caller's transaction. Errors require rolling back the entire transaction.
// Existing source mapping/state changes share that transaction with Save.
type Writer interface {
	Save(context.Context, []model.HazardEvent) ([]model.HazardEvent, error)
}
