# Domains

Groups the API by feature: [logs](logs/README.md), [traces](traces/README.md), and [metadata](metadata/README.md). [Common](common/README.md) holds shared mock helpers.

Each feature uses three small layers:

- `controller/` selects mock or query handlers and wires endpoint-specific response formatting.
- `model/` provides query entry points, mock records, or result merging.
- `view/` shapes responses, such as filling empty time buckets.

Request flow: policy check → controller → mock execution or shared SQL compiler → response. Both execution paths receive the same `query.Request`. SQL building lives in [query](../query/README.md); [controller/common](../controller/common/analytics.go) provides separate helpers for request parsing, HTTP limits, and response pagination.
