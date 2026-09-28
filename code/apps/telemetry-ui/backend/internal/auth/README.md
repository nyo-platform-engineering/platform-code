# Authentication

Verifies the caller and returns a trusted `Principal` to `internal/policy`.

- `identity.go`: common principal/interface and explicit local development identity.
- `config.go`: provider configuration and account/domain grants to tenants and permissions.
- `oauth.go`: shared auth service and expired-row cleanup.
- `providers.go`: Google/GitHub configuration and profile lookup.
- `login.go`: login initiation, rate limiting, and one-use callbacks with PKCE.
- `session.go`: session authentication, current-user responses, and logout.
- `cookies.go`: random tokens, token hashes, and browser cookie settings.
- `postgres.go`: bound GORM session/login-attempt queries. State is consumed
  atomically with DELETE RETURNING. Models, pool ownership, and AutoMigrate
  live in `internal/database`.

Provider tokens are used only during login. The browser holds an opaque HttpOnly
session cookie; PostgreSQL holds its hash. Grants are checked on every authenticated
request. See [setup and configuration](../../../AUTHENTICATION.md).
