# Signal Deck telemetry UI

Go and React/TypeScript UI for ClickHouse-backed OpenTelemetry traces and logs.
It includes request/error/latency summaries, one-minute charts, trace spans,
severity filtering, and correlated log navigation.

For the complete local workflow, use the [development guide](../dev/README.md):
ClickHouse and Collector run in Docker; both Go applications and Vite run natively.

The browser never receives ClickHouse credentials. It calls the Go API, which
enforces tenant and permission filters before querying ClickHouse. The
current `AUTH_MODE=local` identity is only for local development and fails
closed for every other mode until OIDC is implemented.

Source is split into `frontend/` and `backend/`. The backend uses Gin because
it remains compatible with Go's `net/http` ecosystem while providing a clear
middleware and route-group layer for the API. Fiber's `fasthttp` foundation is
useful for specialized throughput-heavy services, but adds adaptation around
the standard HTTP tooling used here.

The backend uses domain-first MVC packages for traces, logs, and metadata.
Each domain owns its controllers, SQL queries, and response presentation.
Shared HTTP helpers live in `internal/controller/common`; shared filters,
attribute conditions, tenant predicates, and storage live in `internal/query`.
Domain models use a parameterized ClickHouse query builder with mandatory tenant
scope and allowlisted tables. Routes and access policies remain separate for auditing.
See [the access-control audit](ACCESS_CONTROL.md).

```text
backend/
├── main.go
└── internal/
    ├── auth/
    ├── policy/
    ├── routes/
    ├── controller/common/       # HTTP parsing, previews, execution limits
    ├── query/                   # shared filters, SQL predicates, storage
    ├── view/common/             # preview and bucket presentation helpers
    └── domain/
        ├── traces/{controller,model,view}/
        ├── logs/{controller,model,view}/
        └── metadata/{controller,model,view}/
```

Tests live beside their packages; application access-matrix tests remain at the
backend root. Routes bind domain handler functions directly, passing the store and one shared
query budget; domain controllers have no constructor or instance state.

The frontend uses TanStack Router with file-based, automatically code-split
routes. Add pages under `frontend/src/routes`; the Vite plugin regenerates the
typed route tree. The initial pages are `/`, `/traces`, and `/logs`. Route
guards are only a user-interface concern: the Go API remains responsible for
authentication, tenant isolation, and authorization on every request.

## Run it

For native app development with ClickHouse and the Collector in Docker Compose,
see the [dependency setup](../dev/README.md) and
[local development implementation plan](LOCAL_DEVELOPMENT_PLAN.md).
Run `task dev:setup` and `task dev` from `code/apps`.

An optional UI application image can also be built with Docker (its ClickHouse
connection must be configured separately):

```bash
docker build -t local/telemetry-ui:dev .
docker run --rm -p 8080:8080 local/telemetry-ui:dev
```

For split frontend development, enable the package manager once with
`corepack enable`, run `pnpm install --frozen-lockfile`, then `pnpm dev` under
`frontend`. Start the Go server from `backend` on port 8080; Vite proxies
`/api` to Go. The frontend enforces pnpm 12.6 and asks pnpm to download the
pinned Node 24 runtime when it is not already available.

Configure one shared ClickHouse connection with:

```text
CLICKHOUSE_ADDR=127.0.0.1:9000
CLICKHOUSE_USER=app
CLICKHOUSE_PASSWORD=local-clickhouse-app
```

For separate trace and log backends, override any of those fields per signal:

```text
CLICKHOUSE_TRACES_ADDR=traces-db:9000
CLICKHOUSE_TRACES_USER=trace-reader
CLICKHOUSE_TRACES_PASSWORD=trace-secret
CLICKHOUSE_LOGS_ADDR=logs-db:9000
CLICKHOUSE_LOGS_USER=log-reader
CLICKHOUSE_LOGS_PASSWORD=log-secret
```

Each unset signal setting inherits its shared setting. Empty address/user settings
also inherit; an explicitly empty password is preserved. Identical resolved
settings reuse one pool; different settings open separate pools. The schema stays
`otel.otel_traces` on the trace backend and `otel.otel_logs` on the log backend.

`/readyz` checks database connectivity and read access to both signal tables under
a three-second deadline. Either failure returns 503; `/healthz` remains process
liveness only. Service discovery queries both stores, deduplicates and sorts names,
and returns at most 500 services. Its SQL preview describes both queries.

Standard Go OTLP variables enable backend export:

```text
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector-cluster.monitoring.svc.cluster.local:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=dev,k8s.cluster.name=dev
```

Telemetry is server-side only. The frontend contains no OpenTelemetry SDK and
the application exposes no OTLP ingestion route. The Go process exports
directly to the configured collector over its internal network address. API
requests start server-owned traces; public `traceparent` and baggage headers
are deliberately ignored until a trusted authentication boundary exists.

See [PLAN.md](PLAN.md) for the implementation sequence, access-control rules,
API contracts, and production follow-up work. See [API.md](API.md) for implemented query contracts.

For Kubernetes deployment, use the [Helm chart](helm/README.md), including unified
and separate ClickHouse connection examples and readiness probes.
