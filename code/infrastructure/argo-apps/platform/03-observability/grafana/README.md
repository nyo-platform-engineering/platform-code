# Grafana dashboards

[Platform Overview](http://grafana.localhost/d/platform-overview/platform-overview)
uses the provisioned Mimir data source. Dashboard JSON lives in
`manifests/platform-overview.json`, is packaged as a ConfigMap, and is mounted by
the `platform` file provider in the **Platform** folder. Edit the JSON in Git;
Grafana provisions it as read-only. The existing policy dashboard remains in
the **Policies** folder.

## Verified metric inventory

These metric families were present in live Mimir when this dashboard was built.
The collector configurations deliberately filter metrics, so an exporter having
other metrics does not mean they are stored in Mimir.

| Metric | Dashboard use |
| --- | --- |
| `up` | Health of the five scrape targets |
| `container_cpu_usage_seconds_total` | Container CPU rates in cores |
| `container_memory_working_set_bytes` | Container working-set memory |
| `kube_pod_status_ready` | Ready pods and pods reporting not ready |
| `kube_pod_container_status_restarts_total` | Recent container restarts |
| `argocd_app_info` | Application health and sync state |
| `argocd_app_sync_total` | Sync attempts grouped by phase |
| `policy_report_result` | Policy rule/resource outcomes |
| `target_info` | OpenTelemetry target metadata; no separate panel |

No node capacity, filesystem usage, application HTTP latency, or application
request counter families were present. The dashboard therefore does not infer
capacity utilization or application RED metrics from unrelated series. Traces
and logs are available in ClickHouse and linked through Signal Deck.

## Interpretation

- Namespace selection filters workloads and policy results. Scrape health and
  Argo CD state remain cluster-wide; Argo apps belong to the `argocd` namespace.
- Mimir's scrape interval is set to 60 seconds. CPU panels use a minimum step of
  one minute so Grafana's rate window includes enough samples.
- CPU is consumed cores, not a percentage of unavailable node capacity. Memory
  is working set, not a fraction of limits or node RAM.
- Pod readiness includes completed Jobs because pod phase metrics are not
  collected. A not-ready pod is not automatically a failed service.
- Policy totals count rule/resource results, including Pods and controllers
  separately. Missing metric series remain missing; a zero policy failure total
  is substituted only while the policy scrape target is up.
- Restart and sync increases are Prometheus estimates and may be fractional.

All 17 panel targets were checked against live Mimir and through Grafana's query
API, including the historical queries used by trend panels. Authentication uses
the locally ignored `.env`; no service account token belongs in dashboard JSON
or provisioning files.
