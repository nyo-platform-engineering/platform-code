# Local telemetry API

All routes are under `/api/v1`. The API enforces its principal's tenant and
permission on every query. Local mode uses tenant `local`; passing identity or
tenant headers does not change it. Unsupported authentication modes fail closed.
The browser never receives database credentials or submits SQL.

## Filters

| Parameter | Contract |
| --- | --- |
| `from`, `to` | RFC3339 UTC-compatible timestamps; inclusive start, exclusive end |
| `service` | Exact service name; omitted/empty means all |
| `environment` | Exact resource environment; defaults to `local` |
| `traceId` | 32 lowercase hexadecimal digits, excluding all-zero ID |
| `severity` | Logs: unspecified, trace, debug, info, warn, error, fatal |
| `status` | Traces: `error` or `ok` (not Error); omitted means all |
| `minDurationMs` | Traces: nonnegative milliseconds, maximum 86400000 |
| `limit` | Lists: 1–500, default 100 |
| `offset` | Lists: 0–5000, default 0 |

Time range defaults to the last 30 minutes and cannot exceed 24 hours. Time
parameters retain nanosecond precision in SQL. Queries have five-second context
deadlines and cancellation, four concurrent execution slots, and a bounded
250 ms wait for a slot. Database query limits also apply through the user profile.

## Routes

| Route | Data fields |
| --- | --- |
| `GET /meta` | Service version, local actor, available capabilities |
| `GET /services` | `service` values found in traces or logs (maximum 500) |
| `GET /traces/red` | `bucket`, `requests`, `errors`, `errorRate`, `p50Ms`, `p95Ms`, `p99Ms`, `partial` |
| `GET /traces` | `timestamp`, `traceId`, `spanId`, `service`, `name`, `durationMs`, `status` for server spans |
| `GET /traces/:traceId` | Ordered spans including `parentSpanId`, `message`, `attributes` |
| `GET /logs/volume` | `bucket`, `severity`, `records`, `partial` |
| `GET /logs` | `timestamp`, `traceId`, `spanId`, `service`, `severity`, `body` |

Query responses have `data`, `from`, `to`, and `truncated`. List responses add
`nextOffset` when another page is available within the offset cap. Trace detail
also uses the list cap; the UI requests 500 spans and labels truncated traces.
`traces/red` adds a `summary` object with the same aggregate fields, computed over
the entire requested range. Summary percentiles are not averages of bucket values.

Lists use deterministic ordering and bounded offset pagination. Keep the same
`from` and `to` across pages; new late-arriving records can still shift offsets.
The UI fixes the time window and pauses polling while paging. Cursor pagination
is a future improvement for high-volume streams.

Buckets are UTC minutes. Empty counts are zero, empty percentiles are null, and
edge buckets are marked partial. RED counts only server spans. `errorRate` is a
ratio from 0 to 1; the UI formats it as a percentage. Duration is converted from
ClickHouse's nanoseconds to milliseconds. Severity 0 remains `unspecified`.

## Errors and readiness

- `400`: malformed or out-of-bounds filters (`invalid_query`).
- `403`: missing tenant scope or permission (`forbidden`).
- `429`: query concurrency exhausted (`too_many_queries`).
- `503`: storage/query failure (`query_unavailable`); never substituted with empty data.

`/healthz` checks process liveness. `/readyz` checks access to the trace table.
An empty trace detail returns `200` with an empty data array, allowing the UI to
poll while export is still in progress. SQL errors and credentials are not
returned to the browser.

### Multi-value filters

Repeat query parameters to select multiple values:
`?service=api&service=worker&severity=warn&severity=error`.
Values are ORed within a filter and ANDed across filters. Tenant and time bounds
always apply. Service accepts up to 20 values (128 characters each), severity up
to 7, and status up to 2 (`ok`, `error`). Duplicate values are deduplicated.
An absent or single empty parameter means no restriction; mixing an empty value
with other values returns 400. Service discovery ignores severity/status/duration.

The browser defaults to `go-demo` only when the service parameter is absent;
`service=` explicitly selects all services. Filter selections persist in the URL
and browser history. Changing filters resets pagination.

### Server-side search

`q` applies before pagination to both list and aggregate endpoints. Traces search
operation (server spans), service, and trace ID; logs search message, service,
and trace ID. Matching is case-insensitive literal substring matching: punctuation
and SQL wildcard characters are ordinary text. A full 32-character hexadecimal
trace ID uses exact equality. Use status/severity controls for those fields.
Search accepts one q value, trimmed, empty or 3–256 Unicode characters, without
control characters. Invalid input returns 400.

Tenant, environment, service, time, and existing filters remain ANDed with search.
Service discovery ignores q; the UI removes q from trace detail/correlated-log
requests to show complete context. Searches are explicitly submitted, pause live
updates, reset pagination, and anchor the time window in the URL.

Search SQL has a 3-second execution limit, 2,000,000-row read limit, and 256 MiB
read limit, with overflow mode throw. The existing API deadline and concurrency
limits also apply. Exceeding limits fails the request rather than returning
partial aggregates. These are bounded substring scans, not an indexed full-text
engine; large deployments still need workload-specific schema/index tuning and
load testing. See [ClickHouse query complexity limits](https://clickhouse.com/docs/operations/settings/query-complexity)
and [string search semantics](https://clickhouse.com/docs/sql-reference/functions/string-search-functions).

### Attribute conditions

Repeat the `attr` query parameter with JSON objects, for example:

```json
{"scope":"resource","key":"deployment.environment.name","op":"eq","value":"local"}
{"scope":"span","key":"http.response.status_code","op":"gte","value":"500"}
```

All conditions are ANDed with each other and the existing tenant, environment,
time, service, and text filters, before pagination and aggregation. Resource
attributes work on both signals; use `span` on traces and `log` on logs. Trace
list and RED filters match server spans. Detail and correlated-log requests
remove attribute conditions to show the full trace context. Service discovery
also ignores attribute conditions.

Operators: `eq`, `neq` (case-sensitive); `contains` (case-insensitive literal);
`exists`, `missing` (omit value); and numeric `gt`, `gte`, `lt`, `lte`. Numeric
comparisons exclude non-numeric values. All operators except `missing` require
the key to exist, so empty values and absent keys are distinct.

At most eight conditions are accepted. Keys must contain 1–128 Unicode
characters; values at most 256, without control characters. Numeric values must
be finite. Invalid operators, unknown JSON fields, and signal-incompatible scopes
return 400. Attribute keys and values are bound SQL arguments; attribute-only
queries use the same execution/read/memory budgets as text search. Map queries
remain bounded scans; large deployments should tune schema/indexing using their
actual query workload.

The UI submits conditions explicitly using React Hook Form, persists them in
the URL, anchors the time window, and applies them to rows and charts together.

### JSON field search

Choose **JSON body** on Logs to filter fields inside the log body. For JSON stored
in a resource, span, or log attribute, choose that scope and attribute key, then
fill the optional **JSON path**. Leave the path empty for ordinary attribute matching.

```json
{"scope":"body","key":"items.0.price","op":"gte","value":"10"}
{"scope":"log","key":"payload","path":"user.id","op":"eq","value":"42"}
```

Paths have up to eight dot-separated segments and 128 characters. Numeric segments
use zero-based indexes (maximum 1,000,000). Literal dotted keys and numeric object
keys cannot be addressed with this shorthand; ordinary attribute keys remain literal.
Body conditions use `key` as the path and reject an additional `path`.

String values are decoded before comparison; other values use their JSON representation.
Thus `true`, `null`, and `42` can be entered directly. Equality compares these textual
representations, so JSON number `42` and JSON string `"42"` both match `42`.
Numeric operators accept numbers and numeric strings. Objects/arrays compare using
serialized JSON; prefer paths to individual fields. A JSON null counts as existing;
an absent field does not. Invalid JSON and missing source attributes are excluded
from every JSON condition, including `missing`. Plain-text log bodies remain searchable
with the existing text search.

Extraction uses bound arguments with ClickHouse's
[JSON functions](https://clickhouse.com/docs/sql-reference/functions/json-functions).
The existing tenant/time boundaries, query budgets, URL persistence, and consistent
list/chart filtering also apply. This supports field extraction and filtering, not
arbitrary scripts or an ingestion transformation pipeline.

### SQL preview and query planning

Add `preview=1` to an existing analytics GET endpoint to compile its SQL without
executing it. The same permissions, tenant scope, and filter validation apply.
The response contains `queries` with `name`, `sql`, and ordered `parameters`
(`position`, Go `type`, string `value`), plus the exact `from`/`to` range. String
parameter values preserve Int64 timestamp precision in browsers. RED returns both
its bucket and whole-window summary queries. Responses use `Cache-Control: no-store`.
The SQL uses positional `?` parameters; copying SQL does not inline values.

Use **Preview SQL** below the page filters. It previews committed filters, not
unsubmitted form edits. The displayed range is captured on opening/refreshing;
with live data it is a prospective query, not a query-history record. Row pagination
and chart queries share the range. Copy SQL and parameters separately.

The shared compiler places tenant, time, service, exact trace ID, and applicable
basic signal filters in `PREWHERE`, leaving text/JSON predicates in `WHERE`.
Log queries also constrain `toStartOfFiveMinutes(Timestamp)` to match the leading
sorting key, while retaining exact nanosecond bounds. The exclusive upper bound
is rounded only after subtracting one nanosecond. Latency percentiles use one
`quantilesTDigest(0.50,0.90,0.95,0.99)` aggregate state.

Live `EXPLAIN indexes=1, actions=1` checks verify PREWHERE, the log time-key predicate,
and reuse of one JSON extraction per identical expression. Existing integration tests
verify matching rows and aggregates; no production-scale speedup is claimed from
this small local dataset. Arbitrary JSON paths still require parsing candidate rows;
frequently queried paths may need typed materialized columns after workload profiling.
See [ClickHouse PREWHERE](https://clickhouse.com/docs/sql-reference/statements/select/prewhere).

### Attribute key discovery

`GET /logs/attributes` and `GET /traces/attributes` return sorted distinct `key`
values in the standard data envelope. `scope` accepts `resource` or the endpoint's
signal (`log`/`span`, the default). `keySearch` is a case-insensitive literal
substring, at most 128 characters without controls. The same signal permissions,
tenant, environment, time, service and basic signal filters apply. Text and field
conditions are ignored so suggestions can help build a new condition. Trace keys
come from server spans, matching the request-list semantics.

Discovery reads at most the existing search budget, returns at most 50 keys, and
sets `truncated` when more exist. There is no pagination; narrow `keySearch` to find
keys beyond the first 50. It uses map keys only, without loading attribute values
or parsing log bodies. JSON path discovery is not included.

The UI loads suggestions only while the attribute picker is open, debounces search
by 300 ms and cancels obsolete requests. Custom keys remain available when a key
is absent, results are truncated, or suggestions fail. Selecting a key edits the
form; **Run search** still explicitly applies conditions.

Trace charts show request frequency, error rate, and latency separately. Both RED
buckets and the whole-window summary include `p90Ms` alongside `p50Ms`, `p95Ms`, and
`p99Ms`; empty buckets keep all four percentiles null. Percentiles share one TDigest
aggregate state. Error rate reflects server-span Error status, not log severity.
