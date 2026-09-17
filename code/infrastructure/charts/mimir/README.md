# Local Mimir chart

Runs one Mimir process (`-target=all`) with a 5Gi local-path PVC and 72-hour
retention. This is the custom workload chart for the local infrastructure;
Argo CD references it from `argo-apps/platform/02-observability/02-mimir.yaml`.

Grafana's upstream `mimir-distributed` chart targets microservices. This chart
keeps the local filesystem-only setup without MinIO or Kafka. Loki, Tempo,
Grafana, and Alloy are installed using upstream charts instead.

Change the image and storage size in `values.yaml`. The configuration lives
in `templates/configs.yaml`; changing it updates the Deployment's checksum
annotation and restarts the process. No CPU/memory requests or limits are set.
