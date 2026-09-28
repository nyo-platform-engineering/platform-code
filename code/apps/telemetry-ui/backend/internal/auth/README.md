# Authentication

Verifies the caller and returns a trusted `Principal` to `internal/policy`. This
package uses a small public facade so callers see the authentication contract
without depending on provider, cookie, or persistence mechanics.

```text
auth/
├── auth.go       # public types, permissions, and operations
├── doc.go
├── README.md
└── internal/     # OAuth, sessions, grants, cookies, and persistence
```

## Package map

- `auth.go`: trusted principal, permissions, request context, OAuth lifecycle,
  and control-plane bootstrap facade.
- `internal/principal.go`: principal implementation and explicit
  local-development identity.
- `internal/oauth_config.go`: environment loading, session limits, provider credentials,
  and endpoint security validation.
- `internal/organization_access.go`: organization identity rules, grant validation, and
  identity-to-principal resolution.
- `internal/oauth_service.go`: shared OAuth service lifecycle and expired-row cleanup.
- `internal/oauth_providers.go`: Google/GitHub configuration and verified profile lookup.
- `internal/oauth_handlers.go`: login initiation, rate limiting, and one-use PKCE callbacks.
- `internal/session_handlers.go`: session authentication, renewal, current-user response,
  and logout.
- `internal/secure_cookie.go`: opaque token generation, hashing, and browser cookie policy.
- `internal/control_plane_bootstrap.go`: organization, grant, and permission reconciliation.
- `internal/postgres_store.go`: GORM session, login-attempt, and principal queries. State is
  consumed atomically with `DELETE RETURNING`.

Database models, pool ownership, and AutoMigrate live in `internal/database`.
HTTP route registration lives in `internal/routes`; authorization decisions live
in `internal/policy`. Keeping those boundaries separate prevents OAuth transport,
persistence, and application policy from becoming one dependency cycle.

## Runtime flow

1. `Start` creates a one-use OAuth state and PKCE verifier.
2. `Callback` validates state, exchanges the code, and fetches a verified provider
   identity.
3. `ResolvePrincipal` maps that identity to one organization and permission set.
4. `AuthenticateAndRenew` validates the opaque session and rotates it when due.
5. `internal/policy` authorizes the resulting `Principal` for the requested route.

Provider tokens are used only during login. The browser holds an opaque HttpOnly
session cookie; PostgreSQL holds its hash. Grants are checked on every authenticated
request. See [setup and configuration](../../../AUTHENTICATION.md).
