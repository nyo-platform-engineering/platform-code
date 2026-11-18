package model

import (
	"encoding/json"
	"time"
)

type FieldReportSubmission struct {
	ID            string     `gorm:"type:text;primaryKey"`
	ClientID      string     `gorm:"type:text;not null"`
	PayloadHash   string     `gorm:"type:text;not null"`
	Raw           []byte     `gorm:"type:bytea;not null"`
	AcceptedAt    time.Time  `gorm:"not null;default:clock_timestamp();index:field_reports_pending,priority:2"`
	ProcessedAt   *time.Time `gorm:"index:field_reports_pending,priority:1"`
	CorrelationID string     `gorm:"type:text;not null"`
	TraceParent   string     `gorm:"type:text;not null"`
	TraceState    string     `gorm:"type:text;not null"`
}

type FieldReport struct {
	ReportID    string          `gorm:"type:text;primaryKey"`
	ClientID    string          `gorm:"type:text;not null"`
	PayloadHash string          `gorm:"type:text;not null"`
	Document    json.RawMessage `gorm:"type:jsonb;not null"`
	AcceptedAt  time.Time       `gorm:"not null"`
	ProcessedAt time.Time       `gorm:"not null"`
}
