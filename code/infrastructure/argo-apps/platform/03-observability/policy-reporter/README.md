# Policy report collection

Policy Reporter 3.10.0 watches namespaced PolicyReports and ClusterPolicyReports
across the cluster, including controller-owned resources. It exports current
policy/resource results at `policy-reporter.monitoring:8080/metrics`.
Ownership is executed by the generated native `vpol-require-workload-owner`.
Kyverno does not background-scan its parent after native generation. The
metadata-only `ownership-reporting.yaml` opts the native policy into reporting
with `reports.kyverno.io/enabled: 'true'`; Kyverno retains ownership of its spec.
Argo CD applies these labels after the parent policy and retries while generation
completes. If native policies are deleted/recreated, Argo CD reapplies the opt-in.
The existing cluster OpenTelemetry collector scrapes these gauges every 30 seconds
and sends them to ClickStack. Metric labels retain namespace, policy, rule, kind,
resource name, status, and source; messages and report IDs are not metric labels.

ClickStack retains the scraped metrics for 72 hours. Current-result gauges stop
being emitted when their reports are removed. These are evaluation results, not
unique workload counts or a count of admission attempts: Pods and their
controllers can each have results. Rejected admission requests that do not
produce a report are outside these metrics.

Policy telemetry is available from the HyperDX metrics source. Filter
the `policy_report_result` and `cluster_policy_report_result` metrics by namespace,
policy, rule, resource, and status. A zero
failure count is meaningful only while collection is healthy; reports reflect the
last Kyverno evaluation, not continuous revalidation.

Ownership remains in Audit. This application collects results and does not change
policy enforcement or admission coverage. Argo CD deploys Policy Reporter and
the collectors use the OpenTelemetry configuration from main.

References: [Policy Reporter metrics](https://kyverno.github.io/policy-reporter-docs/policy-reporter/metrics.html)
and [Kyverno reports](https://kyverno.io/docs/guides/reports/).
