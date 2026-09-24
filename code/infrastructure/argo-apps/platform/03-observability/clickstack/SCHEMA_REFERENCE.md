# ClickStack ClickHouse schema reference

ClickStack is no longer deployed by this repository. This file preserves a
snapshot of the schema it created so the future log and trace UI can reuse the
useful storage ideas without depending on HyperDX or its collector.

The snapshot was captured from the disposable `k3d-dev` cluster on
2026-09-24, before removing ClickStack. It is reference material, not a schema
migration.

## Queries used

The ClickHouse pod was resolved first so the queries did not depend on a
generated pod name:

```bash
pod=$(kubectl --context k3d-dev -n monitoring get pod \
  -l app=clickhouse-clickhouse \
  -o jsonpath='{.items[0].metadata.name}')

kubectl --context k3d-dev -n monitoring exec "$pod" -- \
  clickhouse-client --query 'SHOW TABLES FROM default'

kubectl --context k3d-dev -n monitoring exec "$pod" -- \
  clickhouse-client --query 'SHOW CREATE TABLE default.otel_logs'

kubectl --context k3d-dev -n monitoring exec "$pod" -- \
  clickhouse-client --query 'SHOW CREATE TABLE default.otel_traces'

kubectl --context k3d-dev -n monitoring exec "$pod" -- \
  clickhouse-client --query "
    SELECT name, engine, partition_key, sorting_key
    FROM system.tables
    WHERE database = 'default'
      AND name IN (
        'otel_metrics_gauge',
        'otel_metrics_sum',
        'otel_metrics_histogram'
      )
    ORDER BY name
    FORMAT PrettyCompactMonoBlock"
```

## `SHOW TABLES` output

```text
hyperdx_sessions
otel_logs
otel_logs_attr_kv_rollup_15m_mv
otel_logs_kv_rollup_15m
otel_metrics_exponential_histogram
otel_metrics_gauge
otel_metrics_histogram
otel_metrics_sum
otel_metrics_summary
otel_traces
otel_traces_kv_rollup_15m
otel_traces_kv_rollup_15m_mv
```

## `otel_logs` output

```sql
CREATE TABLE default.otel_logs
(
    `Timestamp` DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    `TraceId` String CODEC(ZSTD(1)),
    `SpanId` String CODEC(ZSTD(1)),
    `TraceFlags` UInt8,
    `SeverityText` LowCardinality(String) CODEC(ZSTD(1)),
    `SeverityNumber` UInt8,
    `ServiceName` LowCardinality(String) CODEC(ZSTD(1)),
    `Body` String CODEC(ZSTD(1)),
    `ResourceSchemaUrl` LowCardinality(String) CODEC(ZSTD(1)),
    `ResourceAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `ScopeSchemaUrl` LowCardinality(String) CODEC(ZSTD(1)),
    `ScopeName` String CODEC(ZSTD(1)),
    `ScopeVersion` LowCardinality(String) CODEC(ZSTD(1)),
    `ScopeAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `LogAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `EventName` String CODEC(ZSTD(1)),
    `__hdx_materialized_k8s.cluster.name` LowCardinality(String)
        MATERIALIZED ResourceAttributes['k8s.cluster.name'] CODEC(ZSTD(1)),
    `__hdx_materialized_k8s.container.name` LowCardinality(String)
        MATERIALIZED ResourceAttributes['k8s.container.name'] CODEC(ZSTD(1)),
    `__hdx_materialized_k8s.deployment.name` LowCardinality(String)
        MATERIALIZED ResourceAttributes['k8s.deployment.name'] CODEC(ZSTD(1)),
    `__hdx_materialized_k8s.namespace.name` LowCardinality(String)
        MATERIALIZED ResourceAttributes['k8s.namespace.name'] CODEC(ZSTD(1)),
    `__hdx_materialized_k8s.node.name` LowCardinality(String)
        MATERIALIZED ResourceAttributes['k8s.node.name'] CODEC(ZSTD(1)),
    `__hdx_materialized_k8s.pod.name` LowCardinality(String)
        MATERIALIZED ResourceAttributes['k8s.pod.name'] CODEC(ZSTD(1)),
    `__hdx_materialized_k8s.pod.uid` LowCardinality(String)
        MATERIALIZED ResourceAttributes['k8s.pod.uid'] CODEC(ZSTD(1)),
    `__hdx_materialized_deployment.environment.name` LowCardinality(String)
        MATERIALIZED ResourceAttributes['deployment.environment.name'] CODEC(ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_res_attr_key mapKeys(ResourceAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_res_attr_value mapValues(ResourceAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_scope_attr_key mapKeys(ScopeAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_scope_attr_value mapValues(ScopeAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_log_attr_key mapKeys(LogAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_log_attr_value mapValues(LogAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_lower_body lower(Body) TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 8
)
ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)
TTL toDateTime(Timestamp) + toIntervalDay(3)
SETTINGS
    index_granularity = 8192,
    ttl_only_drop_parts = 1,
    enable_block_number_column = 1,
    enable_block_offset_column = 1
```

## `otel_traces` output

```sql
CREATE TABLE default.otel_traces
(
    `Timestamp` DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    `TraceId` String CODEC(ZSTD(1)),
    `SpanId` String CODEC(ZSTD(1)),
    `ParentSpanId` String CODEC(ZSTD(1)),
    `TraceState` String CODEC(ZSTD(1)),
    `SpanName` LowCardinality(String) CODEC(ZSTD(1)),
    `SpanKind` LowCardinality(String) CODEC(ZSTD(1)),
    `ServiceName` LowCardinality(String) CODEC(ZSTD(1)),
    `ResourceAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `ScopeName` String CODEC(ZSTD(1)),
    `ScopeVersion` String CODEC(ZSTD(1)),
    `SpanAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `Duration` UInt64 CODEC(ZSTD(1)),
    `StatusCode` LowCardinality(String) CODEC(ZSTD(1)),
    `StatusMessage` String CODEC(ZSTD(1)),
    `Events.Timestamp` Array(DateTime64(9)) CODEC(ZSTD(1)),
    `Events.Name` Array(LowCardinality(String)) CODEC(ZSTD(1)),
    `Events.Attributes` Array(Map(LowCardinality(String), String)) CODEC(ZSTD(1)),
    `Links.TraceId` Array(String) CODEC(ZSTD(1)),
    `Links.SpanId` Array(String) CODEC(ZSTD(1)),
    `Links.TraceState` Array(String) CODEC(ZSTD(1)),
    `Links.Attributes` Array(Map(LowCardinality(String), String)) CODEC(ZSTD(1)),
    `__hdx_materialized_rum.sessionId` String
        MATERIALIZED ResourceAttributes['rum.sessionId'] CODEC(ZSTD(1)),
    `SampleRate` UInt64
        MATERIALIZED greatest(toUInt64OrZero(SpanAttributes['SampleRate']), 1)
        CODEC(T64, ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_rum_session_id __hdx_materialized_rum.sessionId
        TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_res_attr_key mapKeys(ResourceAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_res_attr_value mapValues(ResourceAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_span_attr_key mapKeys(SpanAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_span_attr_value mapValues(SpanAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_duration Duration TYPE minmax GRANULARITY 1,
    INDEX idx_lower_span_name lower(SpanName) TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 8
)
ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (ServiceName, SpanName, toDateTime(Timestamp))
TTL toDate(Timestamp) + toIntervalDay(3)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1
```

## Metric-table query output

```text
   ┌─name───────────────────┬─engine────┬─partition_key────┬─sorting_key────────────────────────────────────────────────────────────────────────┐
1. │ otel_metrics_gauge     │ MergeTree │ toDate(TimeUnix) │ ServiceName, MetricName, toStartOfHour(TimeUnix), cityHash64(Attributes), TimeUnix │
2. │ otel_metrics_histogram │ MergeTree │ toDate(TimeUnix) │ ServiceName, MetricName, toStartOfHour(TimeUnix), cityHash64(Attributes), TimeUnix │
3. │ otel_metrics_sum       │ MergeTree │ toDate(TimeUnix) │ ServiceName, MetricName, toStartOfHour(TimeUnix), cityHash64(Attributes), TimeUnix │
   └────────────────────────┴───────────┴──────────────────┴────────────────────────────────────────────────────────────────────────────────────┘
```

Their `SHOW CREATE TABLE` output additionally showed the same retention rule:

```text
otel_metrics_gauge:
  TTL toDateTime(TimeUnix) + toIntervalDay(3)

otel_metrics_sum:
  TTL toDateTime(TimeUnix) + toIntervalDay(3)

otel_metrics_histogram:
  TTL toDateTime(TimeUnix) + toIntervalDay(3)
```

All three use `MergeTree`, `ZSTD(1)`, a min-max time index, and Bloom filters
over attribute keys and values. Metrics now go to Mimir, so these tables are
not part of the replacement architecture.

## Ideas retained in the replacement

- Keep 72 hours of local log and trace data.
- Use daily partitions so expired data can be dropped as whole parts.
- Preserve nanosecond timestamps and trace/span correlation identifiers.
- Compress timestamps with Delta and other columns with ZSTD.
- Index trace IDs and commonly searched attribute keys/values.
- Sort logs around time and service; sort traces around service/span/time.

The pinned cluster OpenTelemetry Collector's ClickHouse exporter owns the
active schema so its `INSERT` statements remain version-compatible. Daemon
collectors only insert and do not race the DDL. It uses database `otel`, tables
`otel_logs` and `otel_traces`, a 72-hour TTL, and JSON attributes on the Altinity
Stable ClickHouse 26.3 image. HyperDX-specific materialized columns, session
tables, key/value rollups, and ClickHouse metric tables are not carried forward.
