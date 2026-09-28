# Authentication

Verifies the caller and returns a trusted `Principal` to `internal/policy`. This
stays one flat Go package because the files collaborate on the same small set of
unexported types. A subdirectory would create a separate package and extra API
surface without establishing a useful dependency boundary.

## Package map

- `principal.go`: trusted principal, permissions, request context, and explicit
  local-development identity.
- `oauth_config.go`: environment loading, session limits, provider credentials,
  and endpoint security validation.
- `organization_access.go`: organization identity rules, grant validation, and
  identity-to-principal resolution.
- `oauth_service.go`: shared OAuth service lifecycle and expired-row cleanup.
- `oauth_providers.go`: Google/GitHub configuration and verified profile lookup.
- `oauth_handlers.go`: login initiation, rate limiting, and one-use PKCE callbacks.
- `session_handlers.go`: session authentication, renewal, current-user response,
  and logout.
- `secure_cookie.go`: opaque token generation, hashing, and browser cookie policy.
- `control_plane_bootstrap.go`: organization, grant, and permission reconciliation.
- `postgres_store.go`: GORM session, login-attempt, and principal queries. State is
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
