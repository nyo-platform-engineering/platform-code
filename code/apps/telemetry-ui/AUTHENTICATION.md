# Google and GitHub login

`AUTH_MODE=oauth` is the default and requires sign-in before accessing the app.
`AUTH_MODE=local` explicitly bypasses login for development.
`MOCK` selects telemetry data independently of authentication.

The backend uses `golang.org/x/oauth2` for provider exchanges and GORM with its PostgreSQL driver (backed by `pgx`) for
the PostgreSQL control plane and AutoMigrate. PostgreSQL owns sessions,
organizations, access grants, permissions, and per-signal datasource assignments.
There is no JWT session layer or auth framework.

## Local setup

From the app directory:

1. Copy `.env.example` to `.env` if you do not already have one. Both `.env` and
   `auth.grants.json` are ignored by Git. The generated local grant file starts
   as `[]`, which denies everyone.
2. Create a Google OAuth web client or GitHub OAuth app and fill its client ID and
   secret into `.env`. Start with Google; leave both GitHub fields blank to keep GitHub disabled.
3. Register these callback URLs, matching `AUTH_ORIGIN` exactly:
   - Google: `http://127.0.0.1:5173/api/v1/auth/google/callback`
   - GitHub: `http://127.0.0.1:5173/api/v1/auth/github/callback`
4. Edit `auth.grants.json` using `auth.grants.example.json` as the format reference.
   Replace the placeholder IDs/domains; only add accounts or domains you trust.
5. Start your container runtime (Docker Desktop, Rancher Desktop, or Podman),
   then start PostgreSQL and the backend (control-plane tables are synchronized on startup):

```sh
task auth:db
cd backend
set -a
. ../.env
set +a
go run .
```

In a second terminal, run `task dev:web` from the app directory and open
http://127.0.0.1:5173. Vite forwards `/api` to the backend, including OAuth callbacks.
Task commands load the app’s `.env` automatically. The Go binary does not parse
`.env`; the shell commands above export its contents when running Go directly.
`AUTH_GRANTS_FILE=../auth.grants.json` is relative to the backend working directory.
For a built frontend served by Go, use origin/registered callbacks on port 8080
and set `WEB_DIST_DIR=../frontend/dist` when starting from `backend/`.

The Compose database and password are for local development only. Production
needs a separately provisioned PostgreSQL database and appropriate TLS settings.

## Access grants

The Helm chart expresses access as an `organizations` list. Each organization has
an ID, display name, telemetry scope, grouped datasource IDs, and
`identityMappings`. The chart generates and mounts `organizations.json`; a values
change rolls the Deployment. Each mapping uses a provider plus exactly one
structured match (`subject`, `email`, or `domain`) and its permissions. Structured
exact matches are used instead of a free-form expression language to keep identity
decisions auditable and prevent accidental wildcard access.

`AUTH_GRANTS_FILE` and the format below remain supported for non-Helm development.
`AUTH_ORGANIZATIONS_FILE` selects the organization-list format and takes
precedence when both variables are present.

Each grant has a provider, tenant, permissions, and exactly one selector:

- `subject`: Google's stable `sub` or GitHub's numeric user ID, stored as a string.
  Usernames and email addresses are not identity keys.
- `googleEmail`: an exact lowercase email, accepted only from a Google profile
  with `email_verified=true`. The session identity remains Google’s stable `sub`.
- `googleDomain`: an exact, lowercase Google Workspace hosted domain. This uses
  Google's authenticated `hd` claim and verified-email flag, not an email suffix
  supplied by the browser. It does not grant access to GitHub accounts.

Subject grants take priority over email grants, which take priority over domain grants. Grants are never merged across
organizations. Valid permissions are `observability:traces:read`,
`observability:logs:read`, and `observability:metadata:read`; metadata access is
included automatically for every allowed account, including service discovery.
Grant `observability:admin:read` only to control-plane administrators; it exposes
the non-secret organization, datasource, assignment, and grant-count inventory.
Admin inventory is restricted to the grant's active organization.
The grant file is bootstrap input: startup replaces only `managed_by=config`
rows and preserves database-managed grants. Authorization reads PostgreSQL on
every request, so database-managed permission or organization changes take effect
without restarting. An empty grant file denies everyone unless database-managed
grants exist. A Google email or domain membership change takes effect on the next login
or session expiration, not by polling Google on each API call.

## Sessions and request flow

1. Login creates a random, ten-minute state cookie and stores its hash plus a PKCE
   verifier in PostgreSQL. Initiations are limited to 60 per minute per replica.
2. The callback checks the browser cookie, consumes the state once, and exchanges
   the authorization code using PKCE. It fetches the account through the provider's
   authenticated profile API; provider tokens are not persisted or sent to JavaScript.
3. A matching access grant creates a new random session cookie. PostgreSQL stores
   only its SHA-256 hash, rotation-family hash, provider identity, display name,
   domain, and server-enforced session timestamps.
4. Sessions have an eight-hour absolute lifetime and a 30-minute server-side
   idle timeout by default. `AUTH_SESSION_LIFETIME` may be 15 minutes through
   12 hours, while `AUTH_IDLE_TIMEOUT` must be at least five minutes and no
   longer than the absolute lifetime.
5. Authenticated requests rotate the opaque session identifier every 15 minutes
   by default (`AUTH_RENEWAL_INTERVAL`). Rotation keeps the original absolute
   expiry, allows 30 seconds for concurrent requests carrying the old identifier,
   and never stores an OAuth provider refresh token. Logout and reauthentication
   revoke the complete rotation family.
6. Google/GitHub may reuse their own login session when absolute expiry requires
   a new authorization flow.
7. Logout requires POST, the exact configured Origin, and `X-Telemetry-CSRF: 1`.
   It deletes the server session before clearing the cookie.

Cookies are HttpOnly and SameSite=Lax, with Secure and `__Host-` names on HTTPS.
HTTP is accepted only for loopback development. API responses use `no-store`.
Provider callbacks are excluded from tracing to avoid recording codes or state.
Missing/expired sessions return 401, insufficient permissions return 403, and
session-store failures return 503. Client tenant headers never grant access.

## Database lifecycle

[database/](backend/internal/database/README.md) owns connections and GORM models.
OAuth startup runs AutoMigrate for the PostgreSQL control-plane tables and indexes.
The database role needs table/index creation and alteration permissions, plus
SELECT/INSERT/UPDATE/DELETE on the control-plane tables. A transaction and advisory lock serialize
concurrent replica startup. After AutoMigrate, required column types, NOT NULL constraints, primary keys, and
valid expiry indexes are checked against the models. Any migration, inspection,
or validation error prevents the HTTP server from starting.
Optionally run `go run . -migrate` to synchronize the schema and exit.

There are no versioned SQL files or ledger checks. An existing
`telemetry_schema_migrations` table is left unused. ClickHouse schemas remain
externally managed and never use AutoMigrate.

All replicas must share PostgreSQL, the same origin, provider credentials, and session settings.
Expired rows are removed periodically. The app does not
store passwords, provider refresh tokens, or a separate user-management database.
When `AUTH_ORIGIN` uses HTTPS, the PostgreSQL URL must use `sslmode=verify-full`.

## Verification

`go test ./...` covers simulated provider exchanges, cookie/state binding, replay,
expiry, grant selection, authorization, and logout. To also check real PostgreSQL,
use a disposable test database (the test runs AutoMigrate):

```sh
AUTH_TEST_DATABASE_URL="$DATABASE_URL" go test ./internal/auth -run TestPostgresSessions
```

Real Google/GitHub sign-in requires your registered OAuth client credentials.
