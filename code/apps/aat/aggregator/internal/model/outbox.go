package model

import (
	"encoding/json"
	"time"
)

type HazardOutbox struct {
	ID            int64           `gorm:"primaryKey;autoIncrement;index:hazard_outbox_pending,where:published_at IS NULL"`
	EventKey      string          `gorm:"type:text;not null;uniqueIndex"`
	CorrelationID string          `gorm:"type:text;not null;default:''"`
	TraceParent   string          `gorm:"type:text;not null;default:''"`
	TraceState    string          `gorm:"type:text;not null;default:''"`
	Payload       json.RawMessage `gorm:"type:jsonb;serializer:json;not null"`
	CreatedAt     time.Time       `gorm:"not null;default:now();autoCreateTime:false"`
	PublishedAt   *time.Time
	LeaseUntil    *time.Time
	Attempts      int `gorm:"not null;default:0"`
	// Lane routes a message: "" goes to the regular subject only, "critical" to
	// the regular and the critical subject, "critical_only" (an alert without a
	// stored event) to the critical subject only. Critical lanes are claimed first.
	Lane string `gorm:"type:text;not null;default:''"`
}

type OutboxMessage struct {
	ID            int64
	Attempts      int
	EventKey      string
	CorrelationID string
	TraceParent   string
	TraceState    string
	Payload       []byte
	Lane          string
}
