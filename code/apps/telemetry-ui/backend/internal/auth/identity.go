package auth

import (
	"context"
	"net/http"
)

type Principal struct {
	Subject     string   `json:"subject"`
	DisplayName string   `json:"displayName"`
	Tenant      string   `json:"tenant"`
	Permissions []string `json:"permissions"`
}

type contextKey string

const principalKey contextKey = "principal"

const (
	MetadataRead = "observability:metadata:read"
	TracesRead   = "observability:traces:read"
	LogsRead     = "observability:logs:read"
)

type LocalAuthenticator struct{}

func (LocalAuthenticator) Authenticate(_ *http.Request) (Principal, error) {
	return Principal{
		Subject:     "local-development",
		DisplayName: "Local developer",
		Tenant:      "local",
		Permissions: []string{MetadataRead, TracesRead, LogsRead},
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

func WithPrincipal(ctx context.Context, actor Principal) context.Context {
	return context.WithValue(ctx, principalKey, actor)
}
func FromContext(ctx context.Context) (Principal, bool) {
	actor, ok := ctx.Value(principalKey).(Principal)
	return actor, ok
}
