# Backend access-control audit

The executable source of truth is [policy.go](backend/internal/policy/policy.go).
[Routes](backend/internal/routes/routes.go) only bind HTTP endpoints to controllers. The global
policy middleware authenticates and authorizes before any controller runs.
Startup rejects missing policies and policies without routes. An unclassified
registered endpoint also fails closed with 403 at runtime.

| Method and path | Access |
| --- | --- |
| GET `/healthz`, `/readyz` | Public liveness/storage readiness |
| GET `/`, `/assets/*filepath` | Public frontend files |
| GET `/api/v1/auth/providers`, `/api/v1/auth/session` | Public provider/session status; session endpoint returns 401 when signed out |
| GET `/api/v1/auth/:provider/login`, `/api/v1/auth/:provider/callback` | Public OAuth entry points; callback requires one-use state and PKCE |
| POST `/api/v1/auth/logout` | Session revocation with explicit Origin/header CSRF checks |
| GET `/api/v1/meta`, `/api/v1/services` | `observability:metadata:read` |
| GET `/api/v1/data-sources` | `observability:metadata:read`; active-organization choices without credentials |
| GET `/api/v1/traces`, `/api/v1/traces/:traceId` | `observability:traces:read` |
| GET `/api/v1/traces/red`, `/api/v1/traces/attributes` | `observability:traces:read` |
| GET `/api/v1/logs`, `/api/v1/logs/volume`, `/api/v1/logs/attributes` | `observability:logs:read` |
| GET `/api/v1/admin/summary` | `observability:admin:read`; non-secret inventory for the active organization |

All protected endpoints require a nonempty authenticated subject and organization scope as
well as the listed permission. Missing identity returns 401; missing scope or
permission returns 403 before parsing queries, generating SQL previews, or calling
storage. Authentication storage failures return 503.
`preview=1` uses the same endpoint policy as execution.

The policy also bounds the public SPA fallback: unmatched GET/HEAD requests
outside `/api` and `/otlp` may serve frontend files/index.html. Reserved namespace
roots and descendants, and other unmatched methods, return 404. This fallback
never calls telemetry controllers. Readiness pings the configured database connections and performs fixed, zero-row
probes against both signal tables without returning telemetry data.

## Identity and data scope

[principal.go](backend/internal/auth/principal.go) implements the current local
identity: subject `local-development`, tenant `local`, all read permissions, including admin.
It ignores client identity/tenant headers. `AUTH_MODE=oauth` enables Google/GitHub
login with PostgreSQL sessions and configurable access grants. See
[authentication setup](AUTHENTICATION.md). Local mode bypasses login and is for
development. Other configured modes fail startup.

OAuth principals are resolved from PostgreSQL access grants and organizations on
every request. Controllers read the trusted principal from request context and reject absent
organization scope. Models bind that scope into every telemetry SQL query, including
service discovery, attribute discovery, RED summaries, and previews. Client
filters do not select a tenant. Metadata access exposes service names from both
signals; this is intentionally granted by metadata permission independently of
trace/log permission. Trace and log data still require their own permissions.

## SQL construction

Domain models compile through the GORM adapter in `internal/query/builder.go`. It allowlists
trace/log tables and requires a nonempty tenant before generating either execution
SQL or a preview. Filter values, attribute keys/values, JSON path segments,
discovery search, and pagination use bound arguments. Compiler errors stop both
preview and execution; unsupported query kinds do not panic.

Projection, grouping, and ordering expressions remain code-owned SQL constants.
The builder is not a general SQL sanitizer: never pass request strings to those
expression methods. Scope/operator validation runs at the model boundary as well
as during HTTP parsing. PREWHERE and WHERE are built independently with their
arguments, rather than inferred by subtracting SQL strings. ClickHouse aggregates,
JSON expressions, query budgets, and the shared preview compiler are preserved.

GORM and its ClickHouse driver now assemble and execute these queries. Small custom
clauses preserve PREWHERE and SETTINGS. Dry-run statement SQL and Vars feed both
previews and execution; interpolated debug SQL is never executed. GORM still
requires safe binding and allowlisting for raw expressions and identifiers;
see [GORM's security guidance](https://gorm.io/docs/security.html).

## Structure and verification

`internal/domain/traces`, `internal/domain/logs`, and `internal/domain/metadata`
each contain `controller/`, `model/`, and `view/` packages. Traces own trace lists,
details, RED, and span attribute discovery; logs own log lists, volume, and log
attribute discovery; metadata owns capabilities and cross-signal services.

`internal/controller/common` handles HTTP filters, scope checks, previews,
pagination, deadlines, and the shared execution budget. Domain controllers supply
their query compiler and chart presentation. `internal/query` contains common
filter types, attribute validation, parameterized tenant predicates, and storage;
`internal/view/common` contains shared presentation helpers. There is no generic
telemetry domain dispatching all signal queries.

From `backend`, run `go test ./...` and `go vet ./...`.
`access_test.go` independently checks every API endpoint against each permission,
with execution and preview, and verifies denied requests never reach storage.
It also checks tenant-header spoofing, absent identity/tenant, unsupported auth
modes, public probes, and reserved/unsupported fallback requests. Policy package
tests check policy coverage and runtime denial for an unclassified route.

With local ClickHouse running, use `task dev:integration` from `code/apps` for
live query/tenant isolation and read-only database credential checks. Domain
unit tests run without ClickHouse; live integration tests are opt-in.

When adding an endpoint, add its controller binding in `internal/routes/routes.go`, its explicit
method/path rule in `internal/policy/policy.go`, and an independent expectation in the access
matrix tests. Update this audit table when the access contract changes.

## Connection boundaries

Trace routes resolve the active organization's `traces` assignment; log routes
resolve its `logs` assignment. PostgreSQL stores non-secret connection metadata
and the name of a `CLICKHOUSE_*` password environment variable populated from a
Kubernetes Secret. Bootstrap imports the existing shared or split environment
configuration, while database-managed assignments override it. Equal resolved
settings reuse one pool. Metadata service discovery requires metadata permission and queries both
connections with the same trusted organization scope. It merges results in the application;
there is no cross-server SQL union. A failure on either side rejects the whole
response. Preview returns both named queries and never opens a database query.
Readiness checks connectivity and the expected table on every assigned datasource.
