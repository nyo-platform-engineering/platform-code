# Independent ClickHouse chart

This chart owns the ClickHouse and Keeper custom resources separately from
ClickStack. Edit `schema.sql` in the platform values file to add idempotent
databases, tables, materialized views, and indexes. Argo CD replaces the schema
Job when its rendered input changes.

The `app` and `otelcollector` passwords must match ClickStack's external
connection and exporter credentials. The checked-in values are only for the
disposable local cluster.

ClickStack's bundled collector still creates and writes its standard `default.otel_*`
tables. Custom ingestion tables require a separate collector/export pipeline;
HyperDX can query them after adding or changing its source definitions.
