# Telemetry UI Helm chart

Deploys the API and built frontend from the app's Docker image. ClickHouse is an
external dependency; this chart does not install databases or create credentials.
The default Service is ClusterIP. Gateway API HTTPRoute support is optional.

## Install with one ClickHouse connection

Build/push the [application image](../Dockerfile) to a registry your cluster can
reach. Set `image.repository` and `image.tag`, or pin `image.digest`.
The default `telemetry-ui:0.1.0` is a placeholder for your own image.

Provision a Secret named `telemetry-ui-clickhouse` containing key `password` in
the release namespace, for example through your existing secret manager. Configure
the address and username in a values file; keep the password in the Secret.
From the `telemetry-ui` directory:

```sh
helm upgrade --install telemetry-ui ./helm \
  --namespace monitoring --create-namespace \
  -f ./helm/examples/unified.yaml \
  --set image.repository=registry.example.com/platform/telemetry-ui \
  --set-string image.tag=0.1.0
```

## Separate trace and log connections

Set `clickhouse.type: split`, or use `examples/split.yaml`. Provision Secrets
`telemetry-ui-traces` and `telemetry-ui-logs`, each with key `password`. All Secret
references must be in the release namespace. Override their names/keys as needed.

Choose the connection mode explicitly:

| `clickhouse.type` | Active settings | Emitted environment variables |
| --- | --- | --- |
| `unified` (default) | `clickhouse.unified` | `CLICKHOUSE_ADDR/USER/PASSWORD` |
| `split` | `clickhouse.traces` and `clickhouse.logs` | `CLICKHOUSE_TRACES_*` and `CLICKHOUSE_LOGS_*` |

Only the selected mode's settings and Secret references are rendered. Each active
connection requires an address, username, and password Secret name/key. Split mode
has no implicit shared fallback. Inactive connection blocks are ignored, so switching
modes does not require removing their values. Unsupported types and incomplete
active settings fail validation. An empty password stored in a Secret is supported.
The backend reuses one pool whenever the resolved connection settings are identical.

The expected tables are `otel.otel_traces` on the trace connection and
`otel.otel_logs` on the log connection, using the native ClickHouse port (9000).

## Health and access

Startup/liveness use `/healthz`; database outages do not trigger liveness restarts.
Readiness uses `/readyz`, which checks connectivity and read access to both tables
in unified or split mode. Its five-second probe timeout exceeds the backend's
three-second readiness deadline. Either database failing removes the pod from
Service endpoints. Service discovery queries and merges both backends.

The application currently supports only `AUTH_MODE=local`, granting the fixed local
identity read permissions. For external access, enforce authentication at your
existing gateway. Enable HTTPRoute with `-f ./helm/examples/gateway.yaml` after
configuring its parent Gateway and hostname. Gateway API CRDs must already exist;
TLS and access control are configured on the Gateway, outside this chart.

For internal access:

```sh
kubectl -n monitoring port-forward svc/telemetry-ui-telemetry-ui 8080:8080
```

Open http://localhost:8080. Set `fullnameOverride` to customize resource names.

## Configuration and validation

See [values.yaml](values.yaml) for resources, probes, scheduling, image pull
Secrets, and pod security settings. The image runs as UID/GID 65532 with a read-only
root filesystem and writable `/tmp`; Kubernetes API credentials are not mounted.
`extraEnv` accepts standard Kubernetes environment entries, including `valueFrom`
for Secrets, and can configure standard `OTEL_*` export settings. Managed app and
ClickHouse variables must use their dedicated values. After rotating an env-backed
Secret, restart the Deployment to load the new value.

```sh
helm lint ./helm
helm template telemetry-ui ./helm -f ./helm/examples/unified.yaml
helm template telemetry-ui ./helm -f ./helm/examples/split.yaml -f ./helm/examples/gateway.yaml
```
