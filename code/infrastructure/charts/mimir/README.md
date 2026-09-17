# Local Mimir chart

Mimir stores metrics so you can query them in Grafana. This chart runs one
Mimir process with local filesystem storage, a 5Gi persistent volume, and
72-hour retention. It keeps the metrics backend small for this laptop demo.

The [platform Application](../../argo-apps/platform/02-observability/02-mimir.yaml)
connects this chart to Argo CD. Open Grafana using `task links` from
`code/infrastructure`; its Mimir data source is provisioned by the platform.

## What to change

| Location | Purpose |
| --- | --- |
| [`values.yaml`](values.yaml) | Mimir image and storage size. |
| [`templates/configs.yaml`](templates/configs.yaml) | Mimir runtime configuration and retention. |

The process runs from `/data`, its writable volume mount, so its non-root user
can create the activity log. Config changes update a Deployment checksum and
trigger a restart. No CPU/memory requests or limits are configured.

## Preview a change

From `code/infrastructure`:

```bash
helm lint charts/mimir
helm template mimir charts/mimir -n monitoring
```

These commands inspect the chart without changing the cluster. Push changes
to the GitHub repository Argo CD reads, then inspect the Mimir Application.

## Storage and scope

This is a single-process setup (`-target=all`), rather than Mimir's distributed
microservice layout. It doesn't need MinIO or Kafka for metrics storage;
GitLab uses its own MinIO service elsewhere in the stack.

Stopping the cluster preserves stored metrics. Deleting the cluster removes
local volumes. This chart is intended for local exploration, not a highly
available metrics backend.

For metric filters and example queries, see
[the observability guide](../../README.md#observability).
[Back to the repository guide](../../../../README.md).
