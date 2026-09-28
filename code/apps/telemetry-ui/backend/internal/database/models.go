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
	TokenHash     string     `gorm:"type:text;primaryKey;not null"`
	FamilyHash    string     `gorm:"type:text;not null;default:'';index:telemetry_sessions_family"`
	Provider      string     `gorm:"type:text;not null"`
	Subject       string     `gorm:"type:text;not null"`
	DisplayName   string     `gorm:"type:text;not null"`
	GoogleEmail   string     `gorm:"type:text;not null;default:''"`
	GoogleDomain  string     `gorm:"type:text;not null;default:''"`
	ExpiresAt     time.Time  `gorm:"type:timestamptz;not null;index:telemetry_sessions_expiry"`
	LastSeenAt    time.Time  `gorm:"type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	IdleExpiresAt time.Time  `gorm:"type:timestamptz;not null;default:CURRENT_TIMESTAMP;index:telemetry_sessions_idle_expiry"`
	RenewAfter    time.Time  `gorm:"type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	ReplacedAt    *time.Time `gorm:"type:timestamptz"`
}

func (Session) TableName() string { return "telemetry_sessions" }

// Organization is the authorization and telemetry isolation boundary.
type Organization struct {
	ID             string `gorm:"type:text;primaryKey;not null"`
	Name           string `gorm:"type:text;not null"`
	TelemetryScope string `gorm:"type:text;uniqueIndex;not null"`
}

func (Organization) TableName() string { return "telemetry_organizations" }

// AccessGrant maps a verified provider selector to one organization.
type AccessGrant struct {
	ID             string `gorm:"type:text;primaryKey;not null"`
	Provider       string `gorm:"type:text;not null;uniqueIndex:telemetry_grant_selector"`
	SelectorType   string `gorm:"type:text;not null;uniqueIndex:telemetry_grant_selector"`
	SelectorValue  string `gorm:"type:text;not null;uniqueIndex:telemetry_grant_selector"`
	OrganizationID string `gorm:"type:text;not null;index"`
	ManagedBy      string `gorm:"type:text;not null;default:'database'"`
}

func (AccessGrant) TableName() string { return "telemetry_access_grants" }

type AccessGrantPermission struct {
	GrantID    string `gorm:"type:text;primaryKey;not null"`
	Permission string `gorm:"type:text;primaryKey;not null"`
}

func (AccessGrantPermission) TableName() string { return "telemetry_access_grant_permissions" }

// DataSource contains non-secret connection metadata. PasswordEnv names an
// environment variable populated from a Kubernetes Secret.
type DataSource struct {
	ID          string `gorm:"type:text;primaryKey;not null"`
	Address     string `gorm:"type:text;not null"`
	Database    string `gorm:"type:text;not null;default:'otel'"`
	Username    string `gorm:"type:text;not null"`
	PasswordEnv string `gorm:"type:text;not null"`
	Secure      bool   `gorm:"not null;default:false"`
	ManagedBy   string `gorm:"type:text;not null;default:'database'"`
}

func (DataSource) TableName() string { return "telemetry_data_sources" }

type OrganizationDataSource struct {
	OrganizationID string `gorm:"type:text;primaryKey;not null"`
	Signal         string `gorm:"type:text;primaryKey;not null"`
	DataSourceID   string `gorm:"type:text;primaryKey;not null;index"`
	ManagedBy      string `gorm:"type:text;not null;default:'database'"`
}

func (OrganizationDataSource) TableName() string { return "telemetry_organization_data_sources" }
