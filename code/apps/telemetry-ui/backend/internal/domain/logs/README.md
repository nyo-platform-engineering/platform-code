# Logs

Handles log listing, volume by severity, and attribute-key discovery.

- `controller/` selects the operation and selects a mock or query handler, and wires response formatting.
- `model/queries.go` accepts log operations and delegates SQL generation to [query](../../query/README.md).
- `model/mock.go` creates log fixtures relative to the request end time and passes them to the shared mock engine.
- `view/` fills missing time buckets for volume responses.

Mock and database paths use the same request filters and tenant. Log records include trace IDs so users can follow a trace into its logs.
