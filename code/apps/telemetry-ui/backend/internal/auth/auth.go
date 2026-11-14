package auth

import (
	"context"

	authinternal "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth/internal"
	"gorm.io/gorm"
)

type (
	Authenticator         = authinternal.Authenticator
	Grant                 = authinternal.Grant
	IdentityMapping       = authinternal.IdentityMapping
	IdentityMatch         = authinternal.IdentityMatch
	LocalAuthenticator    = authinternal.LocalAuthenticator
	OAuth                 = authinternal.OAuth
	OAuthConfig           = authinternal.OAuthConfig
	OrganizationConfig    = authinternal.OrganizationConfig
	Principal             = authinternal.Principal
	RenewingAuthenticator = authinternal.RenewingAuthenticator
)

const (
	MetadataRead = authinternal.MetadataRead
	TracesRead   = authinternal.TracesRead
	LogsRead     = authinternal.LogsRead
	AdminRead    = authinternal.AdminRead
)

func LoadOAuthConfig() (OAuthConfig, error) {
	return authinternal.LoadOAuthConfig()
}

func NewOAuth(config OAuthConfig, db *gorm.DB) (*OAuth, error) {
	return authinternal.NewOAuth(config, db)
}

func BootstrapGrants(ctx context.Context, db *gorm.DB, grants []Grant) error {
	return authinternal.BootstrapGrants(ctx, db, grants)
}

func BootstrapControlPlane(ctx context.Context, db *gorm.DB, organizations []OrganizationConfig, grants []Grant) error {
	return authinternal.BootstrapControlPlane(ctx, db, organizations, grants)
}

func HasPermission(actor Principal, expected string) bool {
	return authinternal.HasPermission(actor, expected)
}

func WithPrincipal(ctx context.Context, actor Principal) context.Context {
	return authinternal.WithPrincipal(ctx, actor)
}

func FromContext(ctx context.Context) (Principal, bool) {
	return authinternal.FromContext(ctx)
}

var (
	_ Authenticator         = LocalAuthenticator{}
	_ RenewingAuthenticator = (*OAuth)(nil)
)
