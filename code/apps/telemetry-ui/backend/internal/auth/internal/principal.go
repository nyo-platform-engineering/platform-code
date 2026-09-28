package authinternal

import (
	"context"
	"net/http"
)

type Principal struct {
	Subject           string   `json:"subject"`
	DisplayName       string   `json:"displayName"`
	OrganizationID    string   `json:"organizationId"`
	OrganizationName  string   `json:"organizationName"`
	OrganizationScope string   `json:"organizationScope"`
	Tenant            string   `json:"tenant"` // Backward-compatible API alias for OrganizationScope.
	Permissions       []string `json:"permissions"`
}

type contextKey string

const principalKey contextKey = "principal"

const (
	MetadataRead = "observability:metadata:read"
	TracesRead   = "observability:traces:read"
	LogsRead     = "observability:logs:read"
	AdminRead    = "observability:admin:read"
)

type LocalAuthenticator struct{}

func (LocalAuthenticator) Authenticate(_ *http.Request) (Principal, error) {
	return Principal{
		Subject:           "local-development",
		DisplayName:       "Local developer",
		OrganizationID:    "local",
		OrganizationName:  "Local",
		OrganizationScope: "local",
		Tenant:            "local",
		Permissions:       []string{MetadataRead, TracesRead, LogsRead, AdminRead},
	}, nil
}

func HasPermission(actor Principal, expected string) bool {
	for _, permission := range actor.Permissions {
		if permission == expected {
			return true
		}
	}
	return false
}

type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}

type RenewingAuthenticator interface {
	AuthenticateAndRenew(http.ResponseWriter, *http.Request) (Principal, error)
}

func WithPrincipal(ctx context.Context, actor Principal) context.Context {
	return context.WithValue(ctx, principalKey, actor)
}
func FromContext(ctx context.Context) (Principal, bool) {
	actor, ok := ctx.Value(principalKey).(Principal)
	return actor, ok
}
