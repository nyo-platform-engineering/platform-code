# Signal Deck telemetry UI

Initial Go and React/TypeScript shell for querying the platform's
OpenTelemetry logs and traces. It is deliberately an initialization slice:
the UI, API contracts, security boundary, and server-side self-telemetry work, while the
ClickHouse analytics endpoints remain explicit `501 Not Implemented` tasks.

The browser never receives ClickHouse credentials. It calls the Go API, which
will enforce tenant and permission filters before querying ClickHouse. The
current `AUTH_MODE=local` identity is only for local development and fails
closed for every other mode until OIDC is implemented.

Source is split into `frontend/` and `backend/`. The backend uses Gin because
it remains compatible with Go's `net/http` ecosystem while providing a clear
middleware and route-group layer for the API. Fiber's `fasthttp` foundation is
useful for specialized throughput-heavy services, but adds adaptation around
the standard HTTP tooling used here.

The frontend uses TanStack Router with file-based, automatically code-split
routes. Add pages under `frontend/src/routes`; the Vite plugin regenerates the
typed route tree. The initial pages are `/`, `/traces`, and `/logs`. Route
guards are only a user-interface concern: the Go API remains responsible for
authentication, tenant isolation, and authorization on every request.

## Run it

Build and run everything through Docker:

```bash
docker build -t local/telemetry-ui:dev .
docker run --rm -p 8080:8080 local/telemetry-ui:dev
```

For split frontend development, enable the package manager once with
`corepack enable`, run `pnpm install --frozen-lockfile`, then `pnpm dev` under
`frontend`. Start the Go server from `backend` on port 8080; Vite proxies
`/api` to Go. The frontend enforces pnpm 12.6 and asks pnpm to download the
pinned Node 24 runtime when it is not already available.

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
API contracts, and initial ClickHouse queries.
