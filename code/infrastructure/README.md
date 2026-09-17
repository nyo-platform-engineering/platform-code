# Local infrastructure

Windows or macOS + a Linux Docker engine + single-node k3d + Argo CD.
Bootstrap uses Bash 3.2-compatible commands, including the Bash bundled with
macOS and Windows Git Bash. Cluster workloads run inside the runtime's Linux VM.
Use the same Docker context for cluster creation and application image builds.

## Layout

```text
Taskfile.yml                 cluster settings and operational commands
scripts/bootstrap.sh         create k3d and bootstrap Argo CD
argo-apps/
  root.yaml                  root platform Argo CD Application
  platform/                  platform Applications grouped by wave and function
    00-network/traefik/      app.yaml and values.yaml
    01-cd/argocd/            app.yaml and values.yaml
    02-cd/gitlab-services/   local GitLab database, cache, and object storage
    03-cd/gitlab/            official GitLab Helm chart and values
    02-monitoring/           radar/ and kube-state-metrics/ (app.yaml + values.yaml)
    02-observability/        loki/ and tempo/ (app.yaml + values.yaml), 02-mimir.yaml
    02-policy/               kyverno/ (app.yaml + values.yaml)
    03-policy/               policies/ (app.yaml + manifests/)
    03-observability/        grafana/, otel-collector-cluster/, and otel-collector-daemon/ (app.yaml + values.yaml)
    04-network/gateway/      app.yaml and manifests/gateway.yaml
    05-environments/         05-dev-apps.yaml
  dev/                       development Applications, deployment values, Kustomization
charts/
  deployment/                reusable application Deployment/Service/HTTPRoute/PVC chart
  mimir/                     custom single-process Mimir chart
  gitlab-services/            single-instance local GitLab dependencies
examples/                    application and backend route examples
```

Argo CD owns the stack. The Bash script creates the cluster, installs Gateway
API CRDs, bootstraps Argo CD if missing, and registers the root Application.
It does not install or upgrade the other services. Argo CD sync waves deploy
Traefik, Argo CD itself, Loki/Tempo/Mimir/Radar/Kube-state-metrics,
Grafana/OpenTelemetry Collectors, and finally
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

GitLab CE runs at `gitlab.localhost` through the existing Gateway. Run
`task gitlab` to prepare dependency credentials, upload `code/apps` into
the read-only toolbox mount, and import app directories as GitLab projects.
See [GitLab setup](argo-apps/platform/03-cd/gitlab/README.md).
The upstream runner, registry, KAS, and exporter are enabled. Go demo includes
a lint/test/build/deploy pipeline; `task gitlab` provisions its runner,
registry access, and GitOps deployment config.

This follows Argo CD's [app-of-apps pattern](https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/).

## Tools and first run

Install Task, k3d, Helm, Docker CLI, and kubectl on PATH. Windows also needs
Git Bash. No Python, virtualenv, or project-local binaries are required.
Helm 3 and Helm 4 both work and use their normal user-level chart cache.

### Windows

Run tasks from Git Bash. With Rancher Desktop, select **dockerd (Moby)**,
disable its built-in Kubernetes, and start the engine. Rancher Desktop manages
WSL2 in the background; no Ubuntu installation or WSL terminal is needed.
Docker Desktop's Linux engine can also be used. Select one runtime's Docker
context rather than switching engines between commands.

If Task is missing, install it from PowerShell or Git Bash:

```bash
winget install --id Task.Task --exact
```

Reopen Git Bash after installation. `task setup` installs k3d and Helm through
winget; the desktop runtime supplies Docker CLI and kubectl.

### macOS (Intel or Apple Silicon)

With [Homebrew](https://brew.sh/) installed, install Task:

```bash
brew install go-task
```

Colima is the Mac example here. Install its Docker runtime and CLI separately:

```bash
brew install colima docker
colima start --runtime docker
```

The stack also works with other Linux Docker engines, such as Docker Desktop
or Rancher Desktop in **dockerd (Moby)** mode. Runtime installation, startup,
VM settings, and profiles remain yours to manage. This stack creates its own
k3d cluster; any Kubernetes cluster bundled with the runtime is unnecessary.

`task setup` installs k3d, Helm, and kubectl through Homebrew. Use your normal
Terminal with Bash or zsh; Task invokes Bash for bootstrap. No GNU coreutils or
replacement Bash is required. On Apple Silicon, the demo task builds for the
cluster node's architecture rather than assuming amd64; rebuild the image on
that machine instead of reusing an archive from an Intel machine.

### Shared workflow

Check `docker context ls` and `docker info` if the engine is unreachable.
The scripts use your selected Docker connection and do not start or install
a container runtime. From `code/infrastructure` on either OS:

```bash
task setup       # winget on Windows; Homebrew on macOS
# On Windows, reopen Git Bash after new tool installations.
task up          # checks tools, bootstraps the cluster, and prints service links
task gitlab      # import app projects, configure CI, and start the demo pipeline
task status
task password
# Optional: task go-demo builds/imports a local image instead of publishing through CI.
```

### Personal settings and extensions

Use Task 3.44 or newer for YAML-map support. Copy the local-settings example once, then adjust it for your machine:

```bash
cp settings.local.example.yml settings.local.yml
```

For Colima, uncomment `DOCKER_CONTEXT: colima` in `settings.local.yml` (or use the
context name for your Colima profile from `docker context ls`). Leave it unset
to use your selected Docker context or an exported `DOCKER_HOST`. The file also
sets your cluster name and host ports, so `task up`, `task links`, and the
included `task go-demo` use consistent settings without repeating arguments.
The example uses ports 8080/8443; the shared defaults remain 80/443.

Settings resolve from Task command-line overrides, then exported environment/local
settings, then shared defaults. For example, `task links HTTP_PORT=9090`
overrides the local HTTP port for that invocation. `settings.local.yml` applies when
running the infrastructure Taskfile; directly invoking `apps/Taskfile.yml`
uses exported environment variables or command-line overrides instead.
Image-tag changes still require matching GitOps values; a local setting does
not rewrite Argo CD manifests. Cluster image/port changes still require recreating
the cluster, and the existing `task restart` deletes cluster data.

For custom runtime commands, an optional `Taskfile.local.yml` is included
under the `local:` namespace. For example:

```yaml
version: '3'
tasks:
  runtime:
    desc: Start my Colima Docker runtime
    cmds:
      - colima start --runtime docker
```

Then run `task local:runtime` before `task up`. Replace that task with whatever
your runtime needs. Both personal YAML files are ignored by Git; the shared workflow
does not start, stop, or otherwise configure a runtime automatically. Shared
platform-service settings continue to live in the GitOps Helm values files.

Before `task up`, **commit and push this configuration** to the repository
Argo CD reads. Its default is:

```text
https://github.com/nyo-platform-engineering/platform-code.git
revision: main
```

Argo CD reads Git, not your local working directory. Update `argo-apps/root.yaml` and
the Git sources in `argo-apps/platform/**/*.yaml` if the repository or revision
changes (including each Helm Application's `ref: values` source). For a
private repository, configure repository credentials in Argo
CD; never commit access tokens. If the files are not pushed or Git credentials
are missing, bootstrap still starts Argo CD, but its applications cannot sync.
`task up` finishes after registering the root and printing service links; use
`task status` to monitor asynchronous deployment and check live resource sizing.

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
`*.localhost`, add these names against `127.0.0.1` in `/etc/hosts` on macOS or
`C:\Windows\System32\drivers\etc\hosts` on Windows.
The supplied routes use HTTP; 443 is reserved for future TLS routes.
The Kubernetes API binds to 127.0.0.1:6550.

Override conflicting ports in Task:

```bash
task up HTTP_PORT=8080 HTTPS_PORT=8443 API_PORT=6551
```

The same overrides also avoid privileged host ports on macOS runtimes that
require extra permissions for ports 80/443. Use the same HTTP port with
`task links HTTP_PORT=8080`; changing an existing cluster's published ports
requires `task restart` with those overrides and deletes its data.

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

`task status` checks live containers and init containers. k3s reconciliation
can restore system defaults after upgrades/restarts; rerun `task up` to clear
them. Changing chart versions can introduce new defaults, so check resources
after upgrades.

PVC storage requests remain: LGTM requests 16Gi across four local-path PVCs.
Loki, Tempo, and Mimir have 72-hour retention and run as single processes
with filesystem storage. No MinIO or Kafka is deployed. `task down` and
`task restart` delete cluster data.

No requests/limits avoids reservations and CPU throttling; actual RAM usage
still depends on the workload. The container runtime's Linux VM has its own
memory allocation, independent of Kubernetes resource settings. Give the VM
enough RAM for the full stack, including when using Colima on macOS.

## Application layer (`../apps`)

The starter service lives in `../apps/go-demo`: a standard-library Go HTTP
server and Dockerfile. Its Argo CD Application lives in
`argo-apps/dev/go-demo/app.yaml`, and its deployment settings live in
`argo-apps/dev/go-demo/values.yaml`. Both use the reusable Helm chart at
`charts/deployment`; application source directories contain no Kubernetes
manifests. See [its README](../apps/go-demo/README.md) for local execution and
image import. Build and import `local/go-demo:dev1` before Argo CD syncs it:

```bash
task go-demo             # build, save, import into k3d, and restart the deployment
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
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector-cluster.monitoring.svc.cluster.local:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_METRIC_EXPORT_INTERVAL=60000
```

For gRPC, use port 4317. Host applications can use a local tunnel:

```bash
kubectl --context k3d-dev -n monitoring port-forward svc/otel-collector-cluster 4317:4317 4318:4318
```

`otel-collector-cluster` is a single-replica Deployment. It scrapes shared
LGTM backends, kube-state-metrics, and the Argo CD application controller every
60 seconds, and accepts application OTLP logs, metrics, and traces.
`otel-collector-daemon` runs once per node. It reads local pod logs and scrapes
that node's cAdvisor endpoint; it exposes no application OTLP Service.
Both collectors export directly to Loki/Mimir; application traces go to Tempo.
The daemon excludes both collectors' logs to avoid collecting its own output.
Shared scrapes run only in the cluster Deployment, avoiding duplicates as nodes
are added. Keep the cluster collector at one replica unless scrape targets are
partitioned. Grafana provisions Loki/Mimir/Tempo data sources.

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

Argo CD retains only `argocd_app_info` (application health/sync status),
`argocd_app_sync_total` (sync outcomes), and `up` (scrape health). Controller
metrics are enabled without ServiceMonitor or Prometheus Operator resources.

Edit shared scrape allowlists and `filter/minimal` in
`argo-apps/platform/03-observability/otel-collector-cluster/values.yaml`.
Node-metric filters and log collection live beside it in
`otel-collector-daemon/values.yaml`. Logs and traces bypass metric filters.
Loki receives pod and application logs through its native OTLP endpoint.
Pod metadata uses names such as `k8s.namespace.name`, normalized by Loki to
labels such as `k8s_namespace_name`.

This replaces the previous `otel-collector` Application and Service. Update
application OTLP endpoints to `otel-collector-cluster`; Argo CD prunes the old
collector. Collection can pause briefly during the transition. Historical
metrics remain until retention expires; backends and their PVCs are unchanged.

### Minimal Kubernetes metrics

Kube-state-metrics runs with only the pod collector and two metric families
enabled. Its app and values live under `argo-apps/platform/02-monitoring/kube-state-metrics`.
The cluster collector scrapes it; the daemon scrapes its local kubelet's `/metrics/cadvisor` every
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
Each daemon collector scrapes its own node using the downward-API node IP.

Example Grafana queries:

```promql
sum by (namespace, pod) (rate(container_cpu_usage_seconds_total[5m]))
sum by (namespace, pod) (container_memory_working_set_bytes)
kube_pod_status_ready{condition="true"} == 0
increase(kube_pod_container_status_restarts_total[15m])
```

## Cluster policies

Kyverno runs five cluster-wide validation policies in Audit mode: versioned
images, Traefik-only LoadBalancer services, non-privileged containers,
Deployment readiness probes, and this local cluster's no-CPU/memory-sizing
convention. See [policy configuration](argo-apps/platform/README.md).
Audit allows writes and reports violations. Review reports before switching
`spec.validationActions` to `[Deny]`. Pod policies cover init/ephemeral
containers and generate controller checks; PVC storage requests are unaffected.

```bash
kubectl --context k3d-dev get validatingpolicies
kubectl --context k3d-dev get policyreports -A
kyverno test tests/kyverno
```

The policy tests check compliant manifests, violations, init-container tags,
and the Traefik LoadBalancer exception. Use Kyverno CLI v1.19.1.

## Portability checks

Run `bash tests/bootstrap.sh` to check preflight success and failure messages
without modifying a cluster. The script also runs under Bash 3.2. Container
image manifests were checked for both linux/amd64 and linux/arm64; full rollout
validation on a physical Mac is still needed.

## Operations

```bash
task --list
task status
task links
task password
task logs                 # cluster collector
task logs APP=mimir       # also grafana, loki, tempo, etc.
task logs APP=go-demo NAMESPACE=dev
task go-demo              # build, save, import, and restart the demo
task down           # delete cluster and data
```

`task` lists the available commands. `task status` combines pods, Argo CD
application status, CPU/memory usage, and resource-sizing checks. Argo CD refreshes and syncs
automatically. `task logs` defaults to the cluster collector; use `APP=otel-collector-daemon` for node collection. It follows all containers for the selected app; use
`FOLLOW=false` for a snapshot or `TAIL=100` to change the number of lines.
