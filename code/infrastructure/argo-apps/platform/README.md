# Platform Applications

This folder registers the shared services that applications use: networking,
Git hosting and delivery, telemetry, and cluster policies. Argo CD reads the
Application definitions here and deploys their charts or manifests.

For setup, use the [infrastructure guide](../../README.md). For the first-run
walkthrough, start at the [repository guide](../../../../README.md).

## Find a component

Applications are grouped by sync wave and function. Every Application in a
wave folder has the same wave as its folder prefix and annotation.
Upstream Helm apps use a nested component folder with `app.yaml` and
`values.yaml` beside it. Raw resources live in a component's `manifests` folder.
Argo CD reads all these folders recursively from `../root.yaml`.

| Folder | Function | Applications and waves |
| --- | --- | --- |
| `00-network` | Ingress controller | Traefik (0) |
| `01-cd` | Continuous delivery and GitOps reconciliation | Argo CD (1) |
| `02-cd` | Local GitLab dependencies | PostgreSQL/Redis/object storage (2) |
| `03-cd` | Git hosting, CI runner, registry, and supporting GitLab components | GitLab CE (3) |
| `02-monitoring` | Kubernetes resource and topology inspection | Radar/Kube-state-metrics (2) |
| `02-observability` | Observability storage prerequisites | Altinity ClickHouse operator/Mimir (2) |
| `02-policy` | Policy engine | Kyverno (2) |
| `03-policy` | Cluster validation policies | Cluster policies (3) |
| `03-observability` | Telemetry collection, storage, and visualization | ClickHouse/Policy Reporter (3), Grafana/OTel collectors (4) |
| `04-network` | Gateway API listeners | Gateway (4) |
| `05-environments` | Registration of environment-specific Applications | Development apps (5) |

## Understand telemetry collection

Monitoring and observability overlap. Here, Radar helps inspect the cluster;
the observability group uses Altinity-managed ClickHouse for logs and traces,
Mimir for metrics, OpenTelemetry Collector for collection and routing, and
Grafana for metric visualization and ClickHouse log/trace exploration through
Grafana Labs' official ClickHouse data source plugin.

The cluster collector is a single-replica Deployment for shared ClickHouse,
kube-state-metrics, Argo CD, and policy report scrapes, plus application OTLP ingestion.
The daemon collector handles per-node cAdvisor and pod logs. Each scrape runs
every 60 seconds (policy reports every 30 seconds) with a small metric allowlist; shared jobs never run on the
DaemonSet. Both collectors' logs are excluded from pod-log ingestion.

Policy Reporter watches cluster-wide Kyverno reports and exports current results
to Mimir. Use Grafana to inspect current violations,
affected resources, history, and collection health. See the
[collection guide](03-observability/policy-reporter/README.md) for coverage and
the distinction between current reports and historical events.

## Understand cluster policies

Kyverno runs admission and reports controllers only. Five cluster-scoped
ValidatingPolicies start in Audit mode: versioned images, Traefik-only
LoadBalancer services, non-privileged containers (except kube-system),
Deployment readiness probes, and no container CPU/memory sizing for this local
cluster. Readiness exceptions are limited to `gitlab/gitlab-toolbox`,
`kube-system/local-path-provisioner`, and `kyverno/kyverno-reports-controller`: these
background tools have no traffic-readiness probe support in the pinned charts.
Other Deployments in those namespaces still require probes. PVC storage requests are unaffected. These five policies report violations
without blocking workloads. Inspect policy reports and change each policy's
`spec.validationActions` from `[Audit]` to `[Deny]` when ready. The current
`policies.kyverno.io/v1` API avoids deprecated ClusterPolicy resources.
Policies retry while Kyverno CRDs become available because app-of-apps waves
order definitions without waiting for child workload readiness.

Ownership also starts in Audit mode. Every Pod and workload Pod template,
across all namespaces, is checked for `platform.local/owner` with a non-empty
lowercase team name (hyphens allowed). Platform values use `platform-team`;
Go demo configures it under `labels` in its values file. The policy covers
Deployments, DaemonSets, StatefulSets, ReplicaSets, ReplicationControllers,
Jobs, CronJobs, and standalone Pods. It checks template labels so missing
ownership is reported before controllers create Pods. API bookkeeping
resources such as Events or Leases are outside this ownership check.

Kyverno generates a native ValidatingAdmissionPolicy and Audit binding,
covering system and Kyverno workloads even where its webhooks are excluded.
Background scans skip resource filters, and native admission reporting is
explicitly enabled. Labels are not added automatically by admission.

Commit/push the chart, values, and policy changes to the GitHub source Argo CD
reads. Argo CD applies labels to managed workloads and deploys the Audit policy;
no `task up` rerun is required. System workloads managed by k3d rather than
Argo CD may still lack ownership and will appear in reports. Review and address
these system ownership gaps separately before switching to Deny.
Existing Pods are not evicted. Completed Jobs have immutable templates, so old
history may also appear in reports until retired.

Run `task test-labels` after reconciliation for admission smoke tests. It checks
that compliant and invalid Go demo values, standalone Pods, and CronJobs are
admitted in Audit mode in `dev`, `kube-system`, and `kyverno`. Dry runs do not
create persistent workload policy reports; review reports for actual workloads:

```bash
kubectl --context k3d-dev get policyreports -A
kubectl --context k3d-dev get policyreports -A -o yaml
```

To test a reported mismatch, remove the owner from Go demo's values and push
the change. Argo CD still deploys it, and Kyverno reports the violation. Restore
the label afterward. Switch `ownership.yaml` to `[Deny]` only after reviewing
and fixing reported gaps, including system-generated templates. The same
`task test-labels` command then expects invalid workloads to be rejected.

## Understand startup order and health

Traefik is registered before Argo CD's managed Application to start ingress
early. Argo CD itself is already running from bootstrap. Parent app-of-apps
Applications use Argo CD's default health behavior: they do not inherit child
Application health. Children still report their own workload failures. Sync
waves order Application definitions without waiting for child workload health.
Networking spans two folders: Traefik is registered at wave 0 and Gateway at
wave 4. Observability registers Altinity's operator and Mimir at wave 2,
ClickHouse at wave 3, then Grafana and the collection layer at wave 4. The
collectors also wait for ClickHouse DNS and port readiness because parent
Application waves do not wait for child workload health. Folder names show
broad grouping;
`argocd.argoproj.io/sync-wave` remains the setting that controls it.

## Change a component

Gateway's Application and resource live together under `04-network/gateway`:
`app.yaml` references its `manifests` directory. The platform root excludes
`**/manifests/**` and `**/values.yaml`, so child Applications manage raw resources
and Helm values are never treated as Kubernetes manifests.

Upstream Helm values stay beside each component's `app.yaml`, custom workload
charts and their defaults in `../../charts`, and development Application definitions and deployment values
in `../dev`.

Edit a component's `values.yaml` for its settings, or `app.yaml` for its chart
version and sources. For GitLab's wrapper, the dependency version is pinned
in [the local chart](../../charts/gitlab/README.md). Commit and push changes,
then inspect the affected child Application with `task status` or Argo CD.

Start with the [GitLab guide](03-cd/gitlab/README.md) for projects and CI,
or the [observability guide](../../README.md#observability) for filters and queries.
