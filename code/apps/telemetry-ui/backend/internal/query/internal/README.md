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
`CompileQueries` functions reject signal/operation pairs outside that domain before delegating to
the shared `Compile` function.

### 2. Validation and operation selection

`Request.Validate` accepts only the signal/operation matrix above, validates
pagination, and calls `Filter.ValidateScope`. An empty organization scope fails
closed.

`selectionFor` maps each operation to fixed SQL columns, grouping, and ordering.
These expressions are code-owned and never derived from request values. Service
discovery removes record-only filters. RED produces two compiled statements: the
minute buckets and the whole-window summary.

### 3. Predicate planning

The filter is divided into two stages:

- `PREWHERE`: exact time, organization, environment, trace ID, service, severity,
  status, duration, and the log table's five-minute sorting-key predicate;
- `WHERE`: bounded text search and attribute or JSON predicates that require more
  work per candidate row.

All user and identity values become bound arguments. Only allowlisted table,
column, operator, grouping, and ordering expressions enter SQL directly.

### 4. SQL compilation

`selectQuery` builds a GORM statement against one allowlisted telemetry table.
The custom GORM clauses in `gorm.go` place `PREWHERE` before `WHERE` and append
bounded ClickHouse `SETTINGS` when search work needs a resource budget.

GORM runs in dry-run mode during compilation. It creates SQL and ordered bound
variables without opening a ClickHouse connection. The same compiled result is
used by SQL preview and real execution, so preview cannot drift from execution.

### 5. Datasource selection and execution

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
