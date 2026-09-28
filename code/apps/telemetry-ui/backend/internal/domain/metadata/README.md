# Metadata

Provides application metadata and service discovery across traces and logs.

- `controller/metadata.go` returns version, actor, and capability information without querying storage.
- `controller/analytics.go` selects a mock or query handler for each signal, then combines their service results. Either failure rejects the response.
- `model/` delegates SQL generation to [query](../../query/README.md) and merges service names into a sorted, deduplicated list capped at 500.
- `view/` builds the application metadata response.

Service discovery keeps the shared request scope but ignores record-specific filters such as severity, status, search text, and attributes.
