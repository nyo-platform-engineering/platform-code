# Trace and log UI implementation plan

## Baseline completed

- [x] Gin-based Go API, React/TypeScript shell, and multi-stage container build.
- [x] Typed capability metadata and reserved analytics endpoints.
- [x] Explicit authorization permissions for metadata, traces, and logs.
- [x] Local-only development principal; unsupported auth modes fail closed.
- [x] Server-side-only OTLP export with no browser SDK or public ingestion route.
- [x] Go HTTP server tracing, W3C propagation, OTLP export, and trace-correlated logs.
- [x] One-minute bucket is visible in the API metadata and UI.

## Task 1: secure query foundation

- [ ] Add OIDC discovery and JWT validation. Do not trust identity headers
  unless they come from an authenticated, allow-listed proxy.
- [ ] Map immutable subject, tenant, and role claims into a request principal.
- [ ] Add `viewer`, `operator`, and `admin` policy tests. Keep trace and log
  permissions separate.
- [ ] Require a tenant predicate in every ClickHouse query. Never accept raw
  SQL, column names, sort expressions, or attribute keys from the browser.
- [ ] Add query timeouts, maximum time range, result limits, concurrency
  limits, audit records, and cancellation when the client disconnects.
- [ ] Add authenticated API and ingress rate limits before exposing the
  service outside the local environment.
- [ ] Load the read-only ClickHouse password from a Kubernetes Secret and keep
  it out of API responses, browser configuration, spans, and logs.

Definition of done: cross-tenant and missing-claim tests fail closed, and a
query cannot be constructed without an authorized tenant scope.

## Task 2: ClickHouse adapter and contracts

- [ ] Add the official ClickHouse Go client behind a repository interface.
- [ ] Validate `from`, `to`, `service`, and environment filters. Default to 30
  minutes and initially cap queries at 24 hours.
- [ ] Return ordered, gap-filled UTC buckets with a stable JSON contract.
- [ ] Add integration tests against the pinned Altinity image and the actual
  OTel exporter schema.
- [ ] Decide and document the canonical tenant resource attribute before
  enabling non-local access.

Proposed trace RED query, restricted to server spans and one-minute buckets:

```sql
SELECT
    toStartOfMinute(Timestamp) AS bucket,
    ServiceName AS service,
    count() AS requests,
    countIf(StatusCode = 'Error') AS errors,
    errors / requests AS error_rate,
    quantileTDigest(0.95)(Duration) / 1000000 AS p95_ms
FROM otel.otel_traces
WHERE Timestamp >= {from:DateTime64(9)}
  AND Timestamp < {to:DateTime64(9)}
  AND SpanKind = 'Server'
  AND ResourceAttributes['tenant.id'] = {tenant:String}
GROUP BY bucket, service
ORDER BY bucket, service
```

Before shipping, confirm the emitted `SpanKind` and `StatusCode` values with a
fixture trace. `Duration` is treated as nanoseconds and converted to
milliseconds.

Proposed log-volume query using the OTel severity-number bands:

```sql
SELECT
    toStartOfMinute(Timestamp) AS bucket,
    ServiceName AS service,
    multiIf(
        SeverityNumber >= 21, 'fatal',
        SeverityNumber >= 17, 'error',
        SeverityNumber >= 13, 'warn',
        SeverityNumber >= 9, 'info',
        SeverityNumber >= 5, 'debug',
        'trace'
    ) AS severity,
    count() AS records
FROM otel.otel_logs
WHERE Timestamp >= {from:DateTime64(9)}
  AND Timestamp < {to:DateTime64(9)}
  AND ResourceAttributes['tenant.id'] = {tenant:String}
GROUP BY bucket, service, severity
ORDER BY bucket, service, severity
```

Definition of done: `GET /api/v1/traces/red` and
`GET /api/v1/logs/volume` return real, tenant-filtered data in one-minute
buckets and enforce bounded query cost.

## Task 3: usable visualizations

- [ ] Connect service, environment, and time-range controls to URL state.
- [ ] Render request rate, error percentage, and p50/p95/p99 duration without
  hiding missing buckets or partial data.
- [ ] Render stacked severity volume and support severity/service filtering.
- [ ] Add loading, empty, partial, timeout, forbidden, and backend-error states.
- [ ] Make charts keyboard navigable and provide equivalent tabular data.
- [ ] Link RED anomalies to filtered trace search and log buckets to log search.

## Task 4: telemetry quality

- [ ] Add spans around ClickHouse acquisition, query execution, decoding, and
  result shaping without recording SQL literals or credentials.
- [ ] Record query duration, rows read, returned points, cancellation, and
  cache outcome as bounded attributes/metrics.
- [ ] Propagate server-side W3C context to ClickHouse calls and verify query
  spans are children of the incoming API request.
- [ ] Accept inbound trace context only from authenticated, trusted internal
  callers; never accept public baggage or caller-selected trace identifiers.
- [ ] Add frontend error boundaries for user feedback without exporting
  browser telemetry.
- [ ] Retain failed and slow server query traces at the collector policy layer.

## Task 5: deployment

- [ ] Add CI, immutable image publication, and the Argo CD Application only
  after the query and access-control tests pass.
- [ ] Configure ClickHouse read-only credentials, OTLP endpoints, probes,
  non-root/read-only security context, and `telemetry.localhost` routing.
- [ ] Add a NetworkPolicy limiting API egress to ClickHouse, DNS, and the OTel
  collector.
- [ ] Add dashboards and alerts for API errors, slow ClickHouse queries,
  rejected authorization, dropped spans, and exporter failures.
