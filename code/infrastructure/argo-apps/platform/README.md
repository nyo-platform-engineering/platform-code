# Platform Applications

Applications are grouped by sync wave and function. Every Application in a
wave folder has the same wave as its folder prefix and annotation.
Numbered Application filenames also match that wave. Apps may use a nested
component folder with `app.yaml` and their own values or manifests.
Argo CD reads all these folders recursively from `../root.yaml`.

| Folder | Function | Applications and waves |
| --- | --- | --- |
| `00-cd` | Continuous delivery and GitOps reconciliation | Argo CD (0) |
| `01-network` | Ingress controller | Traefik (1) |
| `02-monitoring` | Kubernetes resource and topology inspection | Radar (2) |
| `02-observability` | Telemetry storage backends | Loki/Tempo/Mimir (2) |
| `03-observability` | Telemetry collection and visualization | Grafana/Alloy (3) |
| `04-network` | Gateway API listeners | Gateway (4) |
| `05-environments` | Registration of environment-specific Applications | Development apps (5) |

Monitoring and observability overlap. Here, Radar helps inspect the cluster;
the observability group handles logs (Loki), traces (Tempo), metrics (Mimir),
collection and forwarding (Alloy), and dashboards (Grafana).

Networking spans two folders: Traefik is registered at wave 1 and Gateway at
wave 4. Observability registers storage backends at wave 2, then collection
and dashboards at wave 3. Folder names show synchronization order;
`argocd.argoproj.io/sync-wave` remains the setting that controls it.

Gateway's Application and resource live together under `04-network/gateway`:
`app.yaml` references its `manifests` directory. The platform root excludes
`**/manifests/**`, so only the Gateway Application manages its Gateway resource.

Upstream Helm values stay in `../../values`, custom workload charts in
`../../charts`, and development Application definitions and deployment values
in `../dev`.
