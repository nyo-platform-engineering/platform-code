# Traces

Handles trace listing, span detail, RED metrics (rate, errors, duration), and attribute-key discovery.

- `controller/` selects the operation and selects a mock or query handler, and wires response formatting.
- `model/queries.go` accepts trace operations and delegates SQL generation to [query](../../query/README.md).
- `model/mock.go` creates span fixtures relative to the request end time and passes them to the shared mock engine.
- `view/` fills missing time buckets for RED responses.

Listings, RED metrics, and attribute discovery use server spans. Detail includes all matching spans in time order. RED returns both time buckets and a summary for the whole request window.
