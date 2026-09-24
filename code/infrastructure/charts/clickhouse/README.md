# Altinity-managed ClickHouse chart

This chart owns a single-node Altinity `ClickHouseInstallation` for local
observability. It deliberately omits Keeper because one shard with one replica
does not need coordination. Edit `schema.sql` in the platform values file to
add idempotent custom databases, tables, views, and indexes. Argo CD recreates
the schema Job through a before-sync hook when its rendered input changes.

The OpenTelemetry collectors read the `otelcollector` password from the
chart-managed `clickhouse-credentials` Secret. The checked-in values are only
for the disposable local cluster.

The cluster OpenTelemetry Collector's ClickHouse exporter creates the `otel`
database and log/trace tables with 72-hour retention. Daemon collectors only
insert, avoiding concurrent DDL. Metrics are stored in Mimir instead. The `app`
user is read-only and used by Grafana Labs' official ClickHouse data source.
