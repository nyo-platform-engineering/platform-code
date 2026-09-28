# Common domain helpers

Shared in-memory query behavior for the log and trace mocks.

`mock/` defines records, applies request filters, and handles pagination, attribute discovery, service discovery, and aggregation. It receives `query.Request` directly and returns `query.Result` without building or parsing SQL.

The [log](../logs/model/mock.go) and [trace](../traces/model/mock.go) models supply their own fixtures. Keep reusable mock behavior here and signal-specific fixture data in those models.
