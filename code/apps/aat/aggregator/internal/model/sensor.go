package model

import "time"

// SensorStation is the sensor's own station list, refreshed by the poller and
// used to validate and place every observation.
type SensorStation struct {
	ID        string    `gorm:"column:station_id;type:text;primaryKey"`
	Name      string    `gorm:"type:text;not null"`
	Province  string    `gorm:"type:text;not null"`
	Latitude  float64   `gorm:"type:double precision;not null"`
	Longitude float64   `gorm:"type:double precision;not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

// SensorState is the poller's view of Sensor Pantai: where to resume, whether the
// circuit breaker lets requests through, and whether the data is still fresh.
// Clients are answered from this row, never by calling the sensor.
type SensorState struct {
	Source              string     `gorm:"type:text;primaryKey" json:"source"`
	Cursor              string     `gorm:"type:text;not null;default:''" json:"-"`
	Breaker             string     `gorm:"type:text;not null;default:'closed'" json:"breaker"`
	ConsecutiveFailures int        `gorm:"not null;default:0" json:"consecutive_failures"`
	OpenUntil           *time.Time `json:"open_until"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	LastError           string     `gorm:"type:text;not null;default:''" json:"last_error"`
	GeneratedAt         *time.Time `json:"generated_at"`
	NewestObservedAt    *time.Time `json:"newest_observed_at"`
	Stale               bool       `gorm:"not null;default:false" json:"stale"`
	StaleReason         string     `gorm:"type:text;not null;default:''" json:"stale_reason"`
	Requests            int64      `gorm:"not null;default:0" json:"requests"`
	UpdatedAt           time.Time  `gorm:"not null" json:"updated_at"`
}
