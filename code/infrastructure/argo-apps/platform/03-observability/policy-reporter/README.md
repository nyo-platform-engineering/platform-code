# Policy report collection

Policy Reporter 3.10.0 watches namespaced PolicyReports and ClusterPolicyReports
across the cluster, including controller-owned resources. It exports current
policy/resource results at `policy-reporter.monitoring:8080/metrics`.
The existing cluster OpenTelemetry collector scrapes these gauges every 30 seconds
and sends them to Mimir. Metric labels retain namespace, policy, rule, kind,
resource name, status, and source; messages and report IDs are not metric labels.

New Kyverno result events also go to Loki, including violation messages. Startup
does not replay old reports. The existing Loki retention is 72 hours; historical
events remain after the resource or its report is deleted. Current-result gauges
disappear when their reports are removed. These are evaluation results, not
unique workload counts or a count of admission attempts: Pods and their
controllers can each have results. Rejected admission requests that do not
produce a report are outside this dashboard.

Grafana provisions **Policies / Kyverno Policy Reports** from the Git-managed
dashboard in `../grafana/manifests/kyverno-policy-reports.json`. Open
`http://grafana.localhost/d/kyverno-policy-reports` and filter by namespace or
policy. The dashboard shows current failures, errors, warnings, passing results,
ownership failures, collection health, trends, affected resources, and event
messages. A zero failure count is meaningful only while collection is healthy;
reports reflect the last Kyverno evaluation, not continuous revalidation.

Ownership remains in Audit. This application collects results and does not change
policy enforcement or admission coverage. Argo CD deploys the collector,
dashboard ConfigMap, Grafana provider, and OpenTelemetry configuration from main.

References: [Policy Reporter metrics](https://kyverno.github.io/policy-reporter-docs/policy-reporter/metrics.html)
and [Kyverno reports](https://kyverno.io/docs/guides/reports/).
