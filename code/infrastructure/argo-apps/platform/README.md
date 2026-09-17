# Platform Applications

Applications are grouped by sync wave and function. Every Application in a
wave folder has the same wave as its folder prefix and annotation.
Upstream Helm apps use a nested component folder with `app.yaml` and
`values.yaml` beside it. Raw resources live in a component's `manifests` folder.
Argo CD reads all these folders recursively from `../root.yaml`.

| Folder | Function | Applications and waves |
| --- | --- | --- |
| `00-network` | Ingress controller | Traefik (0) |
| `01-cd` | Continuous delivery and GitOps reconciliation | Argo CD (1) |
| `02-monitoring` | Kubernetes resource and topology inspection | Radar/Kube-state-metrics (2) |
| `02-observability` | Telemetry storage backends | Loki/Tempo/Mimir (2) |
| `03-observability` | Telemetry collection and visualization | Grafana/OpenTelemetry Collector (3) |
| `04-network` | Gateway API listeners | Gateway (4) |
| `05-environments` | Registration of environment-specific Applications | Development apps (5) |

Monitoring and observability overlap. Here, Radar helps inspect the cluster;
the observability group handles logs (Loki), traces (Tempo), metrics (Mimir),
collection and forwarding (OpenTelemetry Collector), and dashboards (Grafana).

Traefik is registered before Argo CD's managed Application to start ingress
early. Argo CD itself is already running from bootstrap. Parent app-of-apps
Applications use Argo CD's default health behavior: they do not inherit child
Application health. Children still report their own workload failures. Sync
waves order Application definitions without waiting for child workload health.
Networking spans two folders: Traefik is registered at wave 0 and Gateway at
wave 4. Observability registers storage backends at wave 2, then collection
and dashboards at wave 3. Folder names show synchronization order;
`argocd.argoproj.io/sync-wave` remains the setting that controls it.

Gateway's Application and resource live together under `04-network/gateway`:
`app.yaml` references its `manifests` directory. The platform root excludes
`**/manifests/**` and `**/values.yaml`, so child Applications manage raw resources
and Helm values are never treated as Kubernetes manifests.

Upstream Helm values stay beside each component's `app.yaml`, custom workload
charts and their defaults in `../../charts`, and development Application definitions and deployment values
in `../dev`.
