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
Domain models use GORM with its ClickHouse driver and mandatory tenant
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

For mock development with backend reloads and frontend hot reload, run from this directory:

```bash
task dev
```

Open http://127.0.0.1:5173. This installs frontend dependencies and starts the
mock API on port 8080 alongside Vite. No containers are started. Ctrl-C stops
the development tasks. Air watches Go source and module files, rebuilding and
restarting the API after saves (500 ms debounce, limited build concurrency).
Build failures stop the previous API so stale code is not served. The pinned
watcher is downloaded through Go on first use; no global install is needed.
Stop any earlier preview using
port 8080 first. Requires Go, Task, and npm. The tasks use npm's cache to run Node 24.11.0
and pnpm 12.6.0 directly, bypassing global Corepack shims. The first run downloads
these tools; frontend setup completes before either development server starts.

Use `task dev:api` or `task dev:web` for separate terminals, `task build` for a
native build, and `task test` for backend/frontend tests. `task dev:mock` is an
alias for `task dev`.


For a lightweight preview without Docker, ClickHouse, or a collector, build the
frontend once and run the Go server with `MOCK=true` (from this directory):

```bash
cd frontend
pnpm install --frozen-lockfile
pnpm build
cd ../backend
GOMAXPROCS=2 go build -p 1 -o /tmp/telemetry-ui .
cd ..
MOCK=true LISTEN_HOST=127.0.0.1 /tmp/telemetry-ui
```

Open http://127.0.0.1:8080. Mock mode serves synthetic local-tenant traces and
correlated logs from the hour before the query end time. Live refreshes move the
fixtures forward with the current time; paused queries and pagination with a fixed
`to` retain stable timestamps. No restart is needed to keep sample data fresh.
Filters, charts, pagination, and trace details work against the in-memory data;
SQL previews still show the real query. Readiness checks the in-memory store.
Mock stores and fixtures live beside each domain model in
`internal/domain/traces/model/mock.go` and `internal/domain/logs/model/mock.go`.
Shared in-memory query helpers live in `internal/domain/common/mock`.
Mock mode opens no ClickHouse connections and disables backend OTLP export.
When OAuth is enabled, its PostgreSQL control plane remains active and receives
an in-memory mock datasource assignment for each configured organization.
`MOCK` defaults to `false`; invalid boolean values fail startup. Helm always sets
`MOCK=false`, exposes no mock value, and rejects `MOCK` through `extraEnv`.


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

For split frontend development, install the pinned package manager with
`npm install --global pnpm@12.6.0`, run `pnpm install --frozen-lockfile`, then `pnpm dev` under
`frontend`. Start the Go server from `backend` on port 8080; Vite proxies
`/api` to Go. The frontend enforces pnpm 12.6 and asks pnpm to download the
pinned Node 24 runtime when it is not already available.

For Google/GitHub login and the PostgreSQL control plane, follow
[authentication setup](AUTHENTICATION.md) and the [control-plane schema](CONTROL_PLANE.md).
Local development without login remains
available only when explicitly enabled with `AUTH_MODE=local`. OAuth sign-in is the default.

Set `MAX_CONCURRENT_QUERIES` to control how many analytics requests each backend
instance can execute at once (default `4`, positive integers only). The limit is
shared by logs, traces, and services, including mocks. Requests wait up to 250 ms
for a slot before receiving HTTP 429; SQL previews bypass the limit. With Helm,
set this environment variable through `extraEnv`.

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

Each unset signal setting inherits its shared setting. On OAuth startup these
settings bootstrap datasource records and per-organization signal assignments in
PostgreSQL; database-managed assignments override bootstrap rows. Password values
remain Kubernetes-injected environment variables and are never stored in
PostgreSQL. Identical resolved settings reuse one pool. The schema stays
`otel.otel_traces` on the trace backend and `otel.otel_logs` on the log backend.
Helm groups reusable datasources under `unified`, `traces`, and `logs`. Each entry
in the organization list keeps its identity mappings, telemetry scope, permissions,
and grouped datasource IDs together. Unified IDs serve both signals;
signal-specific IDs cannot cross signals.

`/readyz` checks database connectivity and read access on every assigned datasource
under a three-second deadline. Any failure returns 503; `/healthz` remains process
liveness only. Service discovery queries the selected signal datasource and returns
at most 500 services.

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

For Kubernetes deployment, use the [Helm chart](helm/README.md), including unified,
separate, and multi-datasource examples and readiness probes.

## Local GitLab and k3d deployment

The GitOps Application is registered under
`code/infrastructure/argo-apps/dev/telemetry-ui`. It reads this app's `helm/` chart
and CI-managed `deploy/values.yaml` from the private local GitLab project
`root/telemetry-ui`, with environment values from the infrastructure repository.

After committing and pushing the infrastructure configuration, run from
`code/infrastructure`:

```sh
task up
task onboard APP=telemetry-ui
```

Onboarding uploads tracked app files, imports the GitLab project, enables CI pushes,
provisions read-only repository/registry deploy credentials, and copies the local
ClickHouse read-only app credential into a Secret in `dev`. Secrets are passed
directly to Kubernetes and are never printed. Existing GitLab source/history and
CI-selected image values are preserved on subsequent imports; push normal source
changes to the GitLab app project after its initial import.

CI checks Go formatting/vet/tests, tests and builds the frontend, lints the chart,
then packages the static binary, frontend, and CA certificates using Crane without
a Docker socket or privileged container. Deploy commits the immutable image digest
back to GitLab; Argo CD performs the rollout. CI only has read access to the app's
Deployment. Visit `http://telemetry.localhost` on your configured cluster HTTP port.
