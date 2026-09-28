# Query

Shared request types, SQL compilation, and ClickHouse access for all domains.

- `Request` carries the filter, operation kind, and authenticated tenant to either the mock store or SQL compiler.
- `Compile` selects fixed SQL definitions and uses the private builder to produce SQL with bound arguments. Request values never become SQL identifiers or expressions.
- `filter.go`, `attribute_filters.go`, and `validation.go` handle filtering and validation. `store.go` owns database connections and execution.

Domain controllers select a backend with `Handler(isMock, mockHandler, queryHandler)` when wiring an endpoint. The [HTTP wrapper](../controller/common/analytics.go) handles previews and resource limits. SQL previews use the same compiler as database queries; mocks do not compile SQL.
