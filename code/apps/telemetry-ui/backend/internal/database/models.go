package database

import "time"

// LoginAttempt stores the state and PKCE verifier for a single OAuth callback.
type LoginAttempt struct {
	TokenHash string    `gorm:"type:text;primaryKey;not null"`
	Provider  string    `gorm:"type:text;not null"`
	Verifier  string    `gorm:"type:text;not null"`
	ExpiresAt time.Time `gorm:"type:timestamptz;not null;index:telemetry_login_attempts_expiry"`
}

func (LoginAttempt) TableName() string { return "telemetry_login_attempts" }

// Session stores an opaque session token's hash and the verified provider identity.
type Session struct {
	TokenHash    string    `gorm:"type:text;primaryKey;not null"`
	Provider     string    `gorm:"type:text;not null"`
	Subject      string    `gorm:"type:text;not null"`
	DisplayName  string    `gorm:"type:text;not null"`
	GoogleDomain string    `gorm:"type:text;not null;default:''"`
	ExpiresAt    time.Time `gorm:"type:timestamptz;not null;index:telemetry_sessions_expiry"`
}

func (Session) TableName() string { return "telemetry_sessions" }
