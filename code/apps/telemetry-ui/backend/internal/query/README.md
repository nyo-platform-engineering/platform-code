# Query

Shared request types, SQL compilation, and ClickHouse access for all domains.

The package root is the supported API used by controllers and domain models.
Implementation-only code lives under `internal/`, where it cannot become an
accidental dependency of the rest of the backend.

```text
query/
├── query.go       # public types, constants, and operations
├── README.md
└── internal/      # validation, SQL building, storage, and routing
```

See the [internal execution pipeline](internal/README.md) for request inputs,
compilation stages, datasource routing, mock behavior, and file ownership.

- `Request` carries a typed signal/operation pair, filters, and authenticated
  organization scope to either the mock store or SQL compiler.
- `Compile` selects fixed SQL definitions and uses GORM with the ClickHouse driver to produce SQL with bound arguments. Request values never become SQL identifiers or expressions.
- `internal/filter.go`, `internal/attribute_filters.go`, and
  `internal/validation.go` handle filtering and validation.
- `internal/store.go` owns database connections and executes/scans results
  through GORM. `internal/routed.go` resolves control-plane datasource choices.
- `internal/gorm.go` adds the ClickHouse `PREWHERE` and `SETTINGS` clauses.
  Compilation uses GORM dry-run statements, so previews never connect to a database.

Domain controllers select a backend with `Handler(isMock, mockHandler, queryHandler)` when wiring an endpoint. The [HTTP wrapper](../controller/common/analytics.go) handles previews and resource limits. SQL previews use the same compiler as database queries; mocks do not compile SQL.

GORM's `Select`, `Order`, `Group`, and raw expressions still require trusted SQL.
Only fixed definitions reach those methods. Request values are passed separately
as arguments; execution uses `Statement.SQL` and `Statement.Vars`, never interpolated
`ToSQL` or `Explain` output. SQL logging is disabled to avoid leaking bound values.
