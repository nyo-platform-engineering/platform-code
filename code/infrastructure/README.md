# Local infrastructure

Git Bash + Rancher Desktop (Moby) + single-node k3d + Argo CD.
Rancher Desktop manages WSL2 in the background; no Ubuntu installation or
WSL terminal is needed. Keep Docker Desktop closed, select **dockerd (Moby)**
in Rancher Desktop, disable its built-in Kubernetes, and start the engine.

## Layout

```text
Taskfile.yml                 cluster settings and operational commands
scripts/bootstrap.sh         create k3d and bootstrap Argo CD
argo-apps/
  root.yaml                  root platform Argo CD Application
  platform/                  platform Applications grouped by wave and function
    00-network/traefik/      app.yaml and values.yaml
    01-cd/argocd/            app.yaml and values.yaml
    02-monitoring/           radar/ and kube-state-metrics/ (app.yaml + values.yaml)
    02-observability/        loki/ and tempo/ (app.yaml + values.yaml), 02-mimir.yaml
    03-observability/        grafana/ and otel-collector/ (app.yaml + values.yaml)
    04-network/gateway/      app.yaml and manifests/gateway.yaml
    05-environments/         05-dev-apps.yaml
  dev/                       development Applications, deployment values, Kustomization
charts/
  deployment/                reusable application Deployment/Service/HTTPRoute/PVC chart
  mimir/                     custom single-process Mimir chart
examples/                    application and backend route examples
```

Argo CD owns the stack. The Bash script creates the cluster, installs Gateway
API CRDs, bootstraps Argo CD if missing, and registers the root Application.
It does not install or upgrade the other services. Argo CD sync waves deploy
Traefik, Argo CD itself, Loki/Tempo/Mimir/Radar/Kube-state-metrics,
Grafana/OpenTelemetry Collector, and finally
the Gateway, followed by the development Application layer. The two-digit
prefix on each platform wave folder equals its `argocd.argoproj.io/sync-wave`
annotation. Apps in the same wave share a prefix. Argo CD uses the annotation
for ordering; wave-folder names make that order visible in the repository.
Wave-folder prefixes match each Application's sync wave. Networking
is split into `00-network` (Traefik) and `04-network` (Gateway); observability
into `02-observability` (storage) and `03-observability` (collection and dashboards).
See [the platform groups](argo-apps/platform/README.md) for
each group's role. The root uses recursive directory discovery and excludes
component `manifests` directories and `values.yaml` files, which their child Applications use.

`argo-apps/platform` contains plain Application manifests. Upstream charts load
their values from this Git repository through Argo CD's
[`$values` source reference](https://argo-cd.readthedocs.io/en/stable/user-guide/multiple_sources/#helm-value-files-from-external-git-repository).
Custom workload templates belong in `charts`; plain manifests and
app-specific deployment values belong in `argo-apps`. Source code and
Dockerfiles belong in `code/apps`.
`argo-apps/dev` contains only development Application definitions; its
Kustomization registers the Go demo from `code/apps/go-demo`. Sync waves order the
Application definitions. Child Application health is not propagated to the
parent: `platform` and `dev-apps` can stay Healthy while a child is Degraded
or Progressing. Each child still reports its own workload health and sync
status. Sync waves order the definitions without waiting for child workloads
to become healthy. Traefik is registered first to start local ingress early.
Gateway API v1.5.1 supplies the standard TLSRoute CRD required by the pinned
Traefik version; bootstrap waits for that CRD before registering the platform.
The system workloads installed by k3s are patched during bootstrap to remove
CPU/memory sizing. Gateway API CRDs remain part of cluster bootstrap.

This follows Argo CD's [app-of-apps pattern](https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/).

## Tools and first run

Use normal Windows installations of Git Bash, Task, k3d, Helm, Docker CLI,
and kubectl on PATH. Docker CLI and kubectl are bundled with the desktop
runtimes. No Python, virtualenv, project-local binaries, or project cache is
required. Helm 3 and Helm 4 both work. Helm still uses its normal user-level
cache when downloading charts.

If Task is missing, install it from PowerShell or Git Bash:

```bash
winget install --id Task.Task --exact
```

Reopen Git Bash after installation. From `code/infrastructure`:

```bash
task setup       # optional: install k3d and Helm using winget
# Reopen Git Bash after new tool installations.
task doctor
task up
task apps
task password
```

Before `task up`, **commit and push this configuration** to the repository
Argo CD reads. Its default is:

```text
https://github.com/nyo-platform-engineering/platform-code.git
revision: main
```

Argo CD reads Git, not your Windows working directory. Update `argo-apps/root.yaml` and
the Git sources in `argo-apps/platform/**/*.yaml` if the repository or revision
changes (including each Helm Application's `ref: values` source). For a
private repository, configure repository credentials in Argo
CD; never commit access tokens. If the files are not pushed or Git credentials
are missing, bootstrap still starts Argo CD, but its applications cannot sync.
`task up` finishes after registering the root; use `task apps` to monitor the
asynchronous deployment. Once all apps are healthy, run `task resources`.

Rerunning `task up` starts/reuses the cluster and registers the root again;
Argo CD handles subsequent configuration updates from Git. Changes to the
k3s image or port mappings require `task restart`, which deletes cluster data.
Every kubectl command explicitly targets `k3d-<CLUSTER_NAME>`.

## Endpoints

Run `task links` to print service URLs and login details. If you changed the
HTTP port, use `task links HTTP_PORT=8080` with the same port used for `task up`.

| URL | Login |
| --- | --- |
| http://argocd.localhost | admin / `task password` |
| http://grafana.localhost | admin / admin |
| http://radar.localhost | No login configured |
| http://traefik.localhost/dashboard/ | No login configured |
| http://go.localhost | Go demo JSON greeting |

Published ports bind to 127.0.0.1. If your browser does not resolve
`*.localhost`, add these names against `127.0.0.1` in the Windows hosts file.
The supplied routes use HTTP; 443 is reserved for future TLS routes.
The Kubernetes API binds to 127.0.0.1:6550.

Override conflicting ports in Task:

```bash
task up HTTP_PORT=8080 HTTPS_PORT=8443 API_PORT=6551
```

Then browse `http://grafana.localhost:8080`, etc. Cluster defaults and the k3s
and Gateway API versions live in `Taskfile.yml`. Child chart versions and
Git sources live in each `argo-apps/platform` Application. Loki, Tempo, and Grafana use the
[upstream community Helm charts](https://github.com/grafana-community/helm-charts),
with `values.yaml` beside each component's `app.yaml` under `argo-apps/platform`.
Their pinned chart
versions retain the existing Loki 3.6.3, Tempo 2.9.0, and Grafana 12.3.2 images.
OpenTelemetry Collector uses its upstream Helm chart and Contrib distribution.
Mimir's image version lives
in `charts/mimir/values.yaml`.

The custom Mimir chart is the one workload-chart exception: the upstream
[`mimir-distributed` chart](https://grafana.com/docs/mimir/latest/set-up/helm-chart/)
targets a distributed deployment. `charts/mimir` keeps this local stack's
single process, filesystem storage, and absence of MinIO/Kafka.

### Existing LGTM installations

This layout replaces the old `lgtm` Application with separate `loki`, `tempo`,
`mimir`, and `grafana` Applications. Do not sync an existing installation
without backing up its monitoring data: pruning the old Application invokes
its deletion finalizer, which deletes its managed resources, including PVCs.
The upstream Loki and Tempo charts also use new StatefulSet PVC names and
storage mount paths; they do not automatically migrate the old volumes.

For disposable local data, recreate the cluster using `task restart` after
pushing the updated configuration. To retain data, back up first, disable
automatic sync on the root, and plan the Application ownership and volume
migration before enabling it again. A fresh cluster needs no migration.

## Local resources and data

One k3s server, zero agents, no node-container memory cap. The pinned Helm
values explicitly remove CPU and memory defaults, including Radar's resource
settings and the OpenTelemetry Collector's resources. The monitoring chart values
and custom Mimir chart have no CPU/memory sizing.
Applications in `../apps` should omit CPU/memory sizing too.

`task resources` checks live containers and init containers. k3s reconciliation
can restore system defaults after upgrades/restarts; rerun `task up` to clear
them. Changing chart versions can introduce new defaults, so check resources
after upgrades.

PVC storage requests remain: LGTM requests 16Gi across four local-path PVCs.
Loki, Tempo, and Mimir have 72-hour retention and run as single processes
with filesystem storage. No MinIO or Kafka is deployed. `task down` and
`task restart` delete cluster data.

No requests/limits avoids reservations and CPU throttling; actual RAM usage
still depends on the workload. Rancher's WSL2 backend has its own memory
ceiling, independent of Kubernetes resource settings.

## Application layer (`../apps`)

The starter service lives in `../apps/go-demo`: a standard-library Go HTTP
server and Dockerfile. Its Argo CD Application lives in
`argo-apps/dev/go-demo/app.yaml`, and its deployment settings live in
`argo-apps/dev/go-demo/values.yaml`. Both use the reusable Helm chart at
`charts/deployment`; application source directories contain no Kubernetes
manifests. See [its README](../apps/go-demo/README.md) for local execution and
image import. Build and import `local/go-demo:dev1` before Argo CD syncs it:

```bash
task go-demo:local       # build, import the image into k3d, and restart the deployment
task go-demo:save        # optional: save a tar archive for offline use
```

To add a workload, copy `examples/application.yaml` into
`argo-apps/dev/<app>/app.yaml`, set the release name and value-file reference,
and create `argo-apps/dev/<app>/values.yaml` with its image and deployment
settings. Register the Application file under `resources` in
`argo-apps/dev/kustomization.yaml`. Never list the Helm values file as a
Kustomize resource. See [the reusable chart](charts/deployment/README.md)
for its configurable Service, probes, routing, storage, and containers.

Keep deployment manifests and configuration under `argo-apps`. Raw platform
manifests such as the Gateway live in their component's `manifests` directory
beside `app.yaml`; reusable Helm templates live in `charts`. The route example in `examples/api-route.yaml`
can be used as a reference for `httpRoute` values instead of putting a route
in the application source directory.

Platform Application definitions go in the appropriate wave/function folder
under `argo-apps/platform`. Upstream Helm chart versions are pinned in
`spec.sources[0].targetRevision`, and their values files live beside each
Application at `<wave-folder>/<component>/values.yaml`.
Bootstrap reads the Argo CD chart version from
`argo-apps/platform/01-cd/argocd/app.yaml`.

Applications send OTLP to OpenTelemetry Collector:

```text
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.monitoring.svc.cluster.local:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_METRIC_EXPORT_INTERVAL=60000
```

For gRPC, use port 4317. Host applications can use a local tunnel:

```bash
kubectl --context k3d-dev -n monitoring port-forward svc/otel-collector 4317:4317 4318:4318
```

The Collector collects Kubernetes pod logs with the chart's file-log preset,
scrapes LGTM backend metrics once per minute, and forwards application OTLP
logs/metrics/traces. Grafana provisions Loki/Mimir/Tempo
data sources. This is backend monitoring, not a full kube-prometheus stack.

Metrics are deliberately minimal. Backend scrapes retain only `up`,
`process_cpu_seconds_total`, `process_resident_memory_bytes`, and `go_goroutines`:
four series per backend instead of thousands. Prometheus metric relabeling drops
other samples before conversion. A shared OpenTelemetry `filter/minimal`
processor also drops other metrics from both scrapes and incoming OTLP.
Applications retain `http.server.request.duration`, `http.server.active_requests`,
and the older `http.server.duration` metric when they emit them. Duration
histograms include request counts and buckets; the allowlist does not limit
label cardinality. `OTEL_METRIC_EXPORT_INTERVAL` sets a 60-second SDK export
interval for applications that support it.

Edit the scrape allowlist and `filter/minimal` in
`argo-apps/platform/03-observability/otel-collector/values.yaml` to expand this
set. Logs and traces bypass the metric filter. The Collector excludes its own
pod logs and forwards other pod and OTLP logs to Loki's native OTLP endpoint.
Pod metadata uses OpenTelemetry names such as `k8s.namespace.name`; Loki
normalizes these to labels such as `k8s_namespace_name`.

When applying this update to an existing cluster, Argo CD replaces the `alloy`
Application with `otel-collector`. Update application OTLP endpoints to the
new service name. The old collector is pruned; telemetry can briefly pause
during the switch. Backends and their PVCs remain managed by their existing
Applications. Historical metrics remain until the configured retention expires.
This single-node setup runs one Collector pod. Adding nodes would duplicate
the static backend and kube-state-metrics scrapes across the DaemonSet; use a separate scraping
Deployment or a target allocator before scaling out.

### Minimal Kubernetes metrics

Kube-state-metrics runs with only the pod collector and two metric families
enabled. Its app and values live under `argo-apps/platform/02-monitoring/kube-state-metrics`.
The Collector scrapes it and the local kubelet's `/metrics/cadvisor` every
60 seconds. Scrape allowlists and the shared OTel filter retain only:

| Source | Metric | Purpose |
| --- | --- | --- |
| Kube-state-metrics | `kube_pod_status_ready{condition="true"}` | One readiness gauge per pod |
| Kube-state-metrics | `kube_pod_container_status_restarts_total` | Restarts per container |
| cAdvisor | `container_cpu_usage_seconds_total` | Total CPU time per container |
| cAdvisor | `container_memory_working_set_bytes` | Working-set memory per container |

Both jobs also keep `up` for scrape health. cAdvisor excludes empty containers,
pod sandboxes, and per-core CPU metrics. Image and runtime name labels are
dropped; container IDs are retained to distinguish containers during restarts.
Kube-state-metrics emits only the allowlisted families; the Collector keeps
only its `true` readiness condition and removes pod UID labels. There are no
node, disk, network, resource request/limit, or kube-state-metrics self metrics.
Series counts scale with pods and containers, rather than a fixed global cap.

cAdvisor uses the Collector's service-account token, validates kubelet TLS
with the cluster CA, and has only `get` permission on `nodes/metrics`.
Each Collector scrapes its own node using the downward-API node IP.

Example Grafana queries:

```promql
sum by (namespace, pod) (rate(container_cpu_usage_seconds_total[5m]))
sum by (namespace, pod) (container_memory_working_set_bytes)
kube_pod_status_ready{condition="true"} == 0
increase(kube_pod_container_status_restarts_total[15m])
```

## Operations

```bash
task --list
task status
task apps
task logs:otel
task logs:mimir
task debug:argocd
task sync           # hard refresh root; automatic sync is enabled
task resources
task down           # delete cluster and data
```
