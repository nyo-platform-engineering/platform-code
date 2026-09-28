# Query

Shared request types, SQL compilation, and ClickHouse access for all domains.

- `Request` carries the filter, operation kind, and authenticated tenant to either the mock store or SQL compiler.
- `Compile` selects fixed SQL definitions and uses GORM with the ClickHouse driver to produce SQL with bound arguments. Request values never become SQL identifiers or expressions.
- `filter.go`, `attribute_filters.go`, and `validation.go` handle filtering and validation. `store.go` owns database connections and executes/scans results through GORM.
- `gorm.go` adds the ClickHouse `PREWHERE` and `SETTINGS` clauses. Compilation uses GORM dry-run statements, so previews never connect to a database.

Domain controllers select a backend with `Handler(isMock, mockHandler, queryHandler)` when wiring an endpoint. The [HTTP wrapper](../controller/common/analytics.go) handles previews and resource limits. SQL previews use the same compiler as database queries; mocks do not compile SQL.

GORM's `Select`, `Order`, `Group`, and raw expressions still require trusted SQL.
Only fixed definitions reach those methods. Request values are passed separately
as arguments; execution uses `Statement.SQL` and `Statement.Vars`, never interpolated
`ToSQL` or `Explain` output. SQL logging is disabled to avoid leaking bound values.
