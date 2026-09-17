# Reusable deployment chart

Deploys one application per Helm release. App-specific configuration belongs
in `argo-apps/<environment>/<app>/values.yaml`; application repositories under
`code/apps` contain source code and Dockerfiles. The Go demo uses this chart
through `argo-apps/dev/go-demo/app.yaml`.

From `code/infrastructure`:

```bash
helm lint charts/deployment -f argo-apps/dev/go-demo/values.yaml
helm template go-demo charts/deployment -n dev -f argo-apps/dev/go-demo/values.yaml
```

| Settings | Purpose |
| --- | --- |
| `image` | Required repository and tag, or digest; pull policy |
| `replicaCount`, `strategy` | Deployment replicas and update strategy |
| `command`, `args`, `containerPorts` | Container entry point and exposed ports |
| `env`, `envFrom` | Variables, Secret/ConfigMap references |
| `readinessProbe`, `livenessProbe`, `startupProbe` | Optional Kubernetes probes |
| `podSecurityContext`, `securityContext` | Pod and container identity/security |
| `service` | Optional Service with configurable type and multiple ports |
| `httpRoute` | Optional Gateway API route, matches, filters, and hostnames |
| `persistence` | Optional PVC or existing claim mounted at `mountPath` |
| `volumes`, `volumeMounts` | Additional Kubernetes volumes and mounts |
| `initContainers`, `sidecars` | Additional containers |
| `imagePullSecrets`, `serviceAccountName` | Existing registry credentials and ServiceAccount |
| `nodeSelector`, `tolerations`, `affinity` | Scheduling configuration |
| `labels`, `annotations`, `podLabels`, `podAnnotations` | Additional metadata |
| `resources` | Optional resource sizing; empty by default |

Resource names default to the release name. Use a unique release for each app;
`fullnameOverride` changes resource names and `nameOverride` changes the app
label/container name. The chart owns `app.kubernetes.io/name` and
`app.kubernetes.io/instance` selector labels; additional labels should use
other keys.

`httpRoute.servicePortName` selects a port by name from `service.ports`; the
route uses that port's number automatically. Routing requires an enabled
Service and at least one Gateway `parentRef`. The platform supplies Gateway
API CRDs; this chart does not install them.

For workloads that do not serve HTTP, disable `service` and `httpRoute`, clear
`containerPorts`, and configure command/args as needed. For persistent apps,
choose a strategy compatible with their access mode (for example `Recreate`
for a single-replica ReadWriteOnce volume). Existing claims are referenced
without creating or managing another PVC.

The chart creates no Secrets or ConfigMaps. Use `envFrom`, `env.valueFrom`, or
additional volumes to reference those managed elsewhere under `argo-apps`.
