package model

import (
	"encoding/json"
	"time"
)

type HazardOutbox struct {
	ID          int64           `gorm:"primaryKey;autoIncrement;index:hazard_outbox_pending,where:published_at IS NULL"`
	EventKey    string          `gorm:"type:text;not null;uniqueIndex"`
	Payload     json.RawMessage `gorm:"type:jsonb;serializer:json;not null"`
	CreatedAt   time.Time       `gorm:"not null;default:now();autoCreateTime:false"`
	PublishedAt *time.Time
	LeaseUntil  *time.Time
	Attempts    int `gorm:"not null;default:0"`
}

type OutboxMessage struct {
	ID       int64
	EventKey string
	Payload  []byte
}
