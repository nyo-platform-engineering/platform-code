# Query execution internals

This package implements telemetry query validation, SQL compilation, ClickHouse
execution, and control-plane datasource routing. Callers use the public facade in
[`query.go`](../query.go); they should not import this package directly.

## Inputs and outputs

The controller supplies a `Request` containing:

- `Signal`: the telemetry source, either `traces` or `logs`;
- `Operation`: the requested shape: `records`, `metrics`, `attributes`,
  `services`, or `detail`;
- `Filter`: time bounds, services, search text, attributes, signal filters, and
  pagination;
- `OrganizationScope`: the authenticated telemetry boundary that is bound to
  `ResourceAttributes['tenant.id']`;
- `DataSourceID`: an optional datasource selected from those assigned to the
  authenticated organization;

The valid pairs are intentionally explicit:

| Signal | Operations |
| --- | --- |
| `traces` | `records`, `metrics`, `attributes`, `services`, `detail` |
| `logs` | `records`, `metrics`, `attributes`, `services` |

Public route names are translated at the controller boundary. For example,
`GET /traces/red` becomes `traces + metrics`, while `GET /logs/volume` becomes
`logs + metrics`. The query layer therefore never encodes the signal and public
route name together in a pseudo-operation string.

Authentication establishes the organization and permissions before this package
is called. Query-string values may populate filters, but they never provide the
organization scope.

Compilation returns one or more `CompiledQuery` values containing a stable name,
SQL with `?` placeholders, and separately bound arguments. Execution returns rows
as `[]map[string]any`. Domain views convert those rows into API responses.

## Execution pipeline

```text
HTTP controller
  │ parses filters and injects authenticated organization scope
  ▼
query.Request
  │
  ├─ mock store ───────────────► in-memory filtering and aggregation
  │
  └─ domain CompileQueries
       ▼
     Compile
       ├─ validate operation, scope, filters, and pagination
       ├─ select fixed columns, grouping, and ordering
       ├─ build PREWHERE and WHERE predicates
       └─ GORM dry run ─────────► CompiledQuery{SQL, Args}
                                      │
                 preview=1 ◄──────────┤
                                      ▼
                              QueryHandler / Execute
                                      │
                    selected datasource stored in context
                                      ▼
                       direct Store or RoutedStore
                                      │
                                      ▼
                                  ClickHouse
```

### 1. Request construction

`internal/controller/common` parses the HTTP request and creates `query.Request`.
The organization scope comes from the authenticated `Principal`. The datasource
ID is syntax-checked at the HTTP boundary and authorization-checked later by
`RoutedStore`.

Domain packages choose which operation compiler is allowed. Their
`CompileQueries` functions reject signal/operation pairs outside that domain
before delegating to the shared `Compile` function.

## Compilation: `Request` to `CompiledQuery`

Compilation is a pure planning step. It validates the request and produces SQL
plus bound arguments, but it does not select credentials, open a connection, or
send anything to ClickHouse.

The complete call chain is:

```text
domain CompileQueries(request)
  │ confirms that the domain owns the signal/operation pair
  ▼
query.Compile(request)                         public facade
  ▼
internal.Compile(request)                      orchestration
  ├─ Request.Validate()                        valid pair and filter limits
  ├─ ServiceFilter()                           only for service discovery
  ├─ selectionFor(request)                     SELECT, GROUP BY, ORDER BY
  ├─ Request.Table() / Request.ServerOnly()    table and span constraint
  ▼
selectQuery(table, selection, filter, scope)
  ├─ Filter.conditions()
  │    ├─ baseWhere()                          PREWHERE text and arguments
  │    └─ lateWhere()                          WHERE text and arguments
  ├─ GORM Table(...).Select(...)
  ├─ custom PREWHERE clause
  └─ optional WHERE and SETTINGS clauses
  ▼
selectBuilder
  ├─ groupBy(...) / orderBy(...)
  ├─ page(...)                                 LIMIT and OFFSET
  └─ containsKey(...)                          attribute discovery only
  ▼
selectBuilder.compile(name)
  ├─ GORM Find() in dry-run mode
  └─ read Statement.SQL and Statement.Vars
  ▼
CompiledQuery{Name, SQL, Args}
```

### Stage A: choose the fixed query shape

`selectionFor` converts the typed pair into code-owned SQL expressions. For
example:

| Request | Table | Selection |
| --- | --- | --- |
| `traces + records` | `otel.otel_traces` | trace row columns, newest first |
| `traces + metrics` | `otel.otel_traces` | minute-bucket RED aggregates |
| `logs + records` | `otel.otel_logs` | log row columns, newest first |
| `logs + metrics` | `otel.otel_logs` | minute and severity counts |
| either signal + `attributes` | signal table | distinct keys from the validated scope |
| either signal + `services` | signal table | distinct service names |

The request chooses only an allowlisted pair. It never supplies a table name,
column expression, grouping expression, or ordering expression.

### Stage B: split cheap and expensive predicates

`Filter.conditions` turns one validated `Filter` into two predicate plans:

```text
(PREWHERE SQL, PREWHERE args, WHERE SQL, WHERE args)
```

The client does not choose a clause. It provides filter values, and the compiler
places them according to fixed server-owned rules:

| Request condition | Emitted clause | Reason |
| --- | --- | --- |
| Time range | `PREWHERE` | Mandatory, usually selective, and aligned with telemetry time ordering |
| Organization scope | `PREWHERE` | Mandatory isolation boundary that should discard other organizations immediately |
| Environment | `PREWHERE` | Fixed resource key and exact comparison |
| Exact trace ID | `PREWHERE` | Cheap exact comparison; a 32-hex-character `q` is treated this way too |
| Service, severity, status, duration | `PREWHERE` | Structured comparisons on known columns |
| Log five-minute time key | `PREWHERE` | Matches the log table's leading sorting-key expression |
| Server-span constraint | `PREWHERE` | Fixed comparison for trace records, metrics, and attribute discovery |
| Ordinary `q` text search | `WHERE` | Substring functions are more expensive than exact structural filters |
| Resource, span, or log attribute condition | `WHERE` | Dynamic map key lookup and comparison |
| JSON body or attribute path | `WHERE` | Requires JSON validation, traversal, extraction, and possibly numeric conversion |
| Attribute key search | `WHERE` | Runs against the flattened key after the base rows are filtered |

`baseWhere` owns the `PREWHERE` side. The intent is to evaluate small, known,
often-selective dimensions first. In a columnar database this can avoid reading
large selected columns, such as log bodies and attribute maps, for rows that have
already failed the time, organization, or service boundary.

`lateWhere` owns ordinary search and attribute conditions. JSON is the clearest
example of why this second stage exists: parsing every log body is expensive, so
the query should first narrow the input by time, organization, environment, and
service. Arbitrary map attributes stay late for the same reason. Search or
attribute predicates also enable bounded ClickHouse `SETTINGS` so an expensive
scan fails instead of returning incomplete data.

Organization and environment are deliberate exceptions to the general
"attributes stay late" rule. They are stored in `ResourceAttributes`, but their
keys are fixed by the application, use exact comparisons, and define structural
query boundaries. User-selected attribute keys and JSON paths remain in `WHERE`.

For example, a trace-record request for organization `acme`, service `checkout`,
error status, and resource attribute `region=ap-southeast-1` is planned roughly
as:

```sql
SELECT <fixed trace record columns>
FROM otel.otel_traces
PREWHERE Timestamp >= fromUnixTimestamp64Nano(?)
  AND Timestamp < fromUnixTimestamp64Nano(?)
  AND ResourceAttributes['tenant.id'] = ?
  AND ResourceAttributes['deployment.environment.name'] = ?
  AND ServiceName IN (?)
  AND StatusCode = 'Error'
  AND SpanKind = 'Server'
WHERE mapContains(ResourceAttributes, ?)
  AND ResourceAttributes[?] = ?
ORDER BY <fixed trace ordering>
LIMIT ?
```

The corresponding arguments remain separate and ordered:

```text
[from, to, "acme", "local", "checkout", "region", "region",
 "ap-southeast-1", limit]
```

Both clause builders return SQL fragments separately from their arguments. A
request value contributes a `?` and an entry in the argument slice; it cannot
become SQL syntax. `PREWHERE` and `WHERE` have the same filtering responsibility
for correctness—the split is an execution-cost decision. ClickHouse retains final
optimizer control, including its own automatic movement of suitable predicates.
See the [ClickHouse PREWHERE guide](https://clickhouse.com/docs/concepts/features/performance/prewhere).

### Stage C: assemble a dry-run GORM statement

`selectQuery` creates a fresh statement from the shared dry-run GORM template and
adds the table, selection, `PREWHERE`, optional `WHERE`, and optional resource
settings. `Compile` then adds grouping, ordering, and the operation-specific
limit:

- record and detail queries request `limit + 1` rows so the response layer can
  detect truncation;
- attribute discovery requests 51 keys to detect the documented 50-key cap;
- service discovery requests at most 500 names;
- aggregate queries do not use list pagination.

The registered GORM clause order is:

```text
SELECT → FROM → PREWHERE → WHERE → GROUP BY → ORDER BY → LIMIT → SETTINGS
```

Calling `Find` renders that statement only. Because the compiler is configured
with `DryRun`, disabled ping, and no connection pool, it cannot contact a database.

### Stage D: freeze the executable plan

`selectBuilder.compile` copies GORM's rendered statement into:

```go
CompiledQuery{
    Name: "traces.metrics",
    SQL:  "SELECT ... PREWHERE ... GROUP BY ...",
    Args: []any{fromNanos, toNanos, organizationScope, ...},
}
```

The SQL still contains `?` placeholders. `Args` preserves the exact placeholder
order and values. Trace metrics additionally compile a second plan named
`traces.metrics.summary`, using the same filters for whole-window totals. All
other operations produce one plan.

The preview path serializes this plan without executing it. The normal path hands
each plan to `Execute`, which calls `store.Query(ctx, SQL, Args...)`. A direct
`Store` runs it on its configured pool; a `RoutedStore` first resolves the
organization-authorized datasource selected in the request context.

## Datasource selection and execution

`QueryHandler` stores `Request.DataSourceID` in the execution context and calls
`Execute` for each compiled statement.

- `Store` uses the directly configured ClickHouse connection. Trace and log
  stores share a pool only when their resolved connection settings match.
- `RoutedStore` reads the authenticated organization from the context, verifies
  that the selected datasource is assigned to that organization and signal, and
  resolves its password through the configured `CLICKHOUSE_*` environment name.
  Multiple assignments require an explicit selection.
- Connection pools are cached by immutable address, database, username,
  password-environment name, and TLS mode.

Readiness probes use `CheckConnection` and `CheckTable`. Routed readiness checks
every assigned datasource rather than only the most recently selected one.

## Preview and mock paths

SQL preview calls the same compiler but stops before `Execute`. It returns SQL and
bound parameter metadata and never resolves a datasource credential or connects
to ClickHouse.

Mock stores implement `MockExecutor`. The controller selects that interface
before compilation, so mock requests use the validated `Request` directly and do
not manufacture or parse SQL. Mock filtering must enforce the same organization,
time, signal, search, attribute, and pagination semantics as the database path.

## Separation of concerns

| File | Responsibility |
| --- | --- |
| `request.go` | Shared request/result contracts and operation-level validation |
| `validation.go` | Organization, signal, and attribute-scope validation |
| `compile.go` | Operation-to-query selection and compilation orchestration |
| `builder.go` | Allowlisted table selection and GORM statement construction |
| `query_plan.go` | PREWHERE/WHERE planning and server-span constraints |
| `filter.go` | Time, organization, signal, and search predicates |
| `attribute_filters.go` | Attribute parsing, validation, and bound JSON/map predicates |
| `gorm.go` | Dry-run compiler and ClickHouse-specific GORM clauses |
| `store.go` | Direct ClickHouse options, pools, execution, and readiness helpers |
| `routed.go` | PostgreSQL datasource catalog, assignments, routing, and pool reuse |

Controllers own HTTP parsing, status codes, concurrency limits, and response
formatting. Domain packages own the operations they expose and their response
views. The auth package owns identity, permissions, and organization resolution.
This package owns only the validated transition from a trusted query request to
bound SQL and database rows.

## Security invariants

- Organization scope is mandatory and cannot come from request headers or query
  parameters.
- Request values are always bound; they never become identifiers or SQL
  expressions.
- Tables, selected columns, operators, grouping, and ordering come from closed
  allowlists.
- Datasource selection is checked against both organization and signal.
- Password values are read from environment references and never stored in the
  control-plane database or returned to the browser.
- Search and attribute scans use bounded execution settings and request-level
  timeouts.
