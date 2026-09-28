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

## Organizations and datasources

`clickhouse.dataSources` defines reusable connections grouped into `unified`,
`traces`, and `logs`. Unified datasources may serve either signal; signal-specific
datasources cannot be assigned across signals. Each organization keeps its own
identity mappings and datasource IDs together:

```yaml
organizations:
  - id: platform
    name: Platform Team
    telemetryScope: platform-prod
    identityMappings:
      - provider: google
        match: {domain: example.com}
        permissions: [observability:traces:read, observability:logs:read]
    dataSources:
      unified: [primary]
      traces: []
      logs: []
```

An identity rule uses exactly one authenticated `subject`, exact lowercase Google
`email`, or exact Google Workspace `domain`. Free-form expressions and wildcards
are intentionally unsupported. A unified datasource ID assigns that connection to
both signals; trace and log IDs apply only to their respective signal.

Each datasource declares a non-secret ID and connection metadata, a `CLICKHOUSE_*`
password environment name, and a Kubernetes Secret reference. Each organization
lists zero or more datasource IDs. The UI selects exactly one
datasource per signal; requests never fan out or silently fail over.

Use the organization list for single, split, or multi-datasource deployments. See
`examples/unified.yaml`, `examples/split.yaml`, and `examples/catalog.yaml`.

OAuth mode mounts the organization list from a generated ConfigMap and loads the
organization, access, and datasource catalog into PostgreSQL. Organization changes
automatically roll the Deployment. Local development uses the first signal-specific
datasource, falling back to the first unified datasource. The backend reuses one
pool whenever resolved connection settings are identical.

The expected tables are `otel.otel_traces` on the trace connection and
`otel.otel_logs` on the log connection, using the native ClickHouse port (9000).

## Health and access

Startup/liveness use `/healthz`; database outages do not trigger liveness restarts.
Readiness uses `/readyz`, which checks connectivity and read access to all assigned
signal datasources. Its five-second probe timeout exceeds the backend's three-second
readiness deadline. Any assigned database failing removes the pod from Service endpoints.

Set `authMode: oauth` for Google/GitHub login. Configure `organizations` in values,
then provide `AUTH_ORIGIN`, `DATABASE_URL`, and provider client IDs/secrets through
`extraEnv` (use `valueFrom.secretKeyRef` for secrets). OAuth startup runs GORM AutoMigrate for PostgreSQL control-plane tables; the database role
needs schema creation and alteration permissions. The chart does not provision
PostgreSQL. ClickHouse schemas remain externally managed.
`authSessionLifetime` defaults to an `8h` absolute limit. `authIdleTimeout`
defaults to `30m`, and `authRenewalInterval` rotates opaque identifiers every
`15m` without extending the absolute limit. HTTPS origins require PostgreSQL
`sslmode=verify-full`.
See [authentication setup](../AUTHENTICATION.md) for identity matching and callback URLs.

The default `authMode: oauth` requires sign-in and at least one matching
`organizations[].identityMappings` rule.
Set `authMode: local` explicitly to bypass login for development. Enable HTTPRoute with `-f ./helm/examples/gateway.yaml` after
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
for Secrets, `OTEL_*` export settings, and dedicated
`CLICKHOUSE_DATASOURCE_*` passwords referenced by PostgreSQL datasource rows.
Managed ClickHouse variables must use their datasource entries. After rotating an env-backed
Secret, restart the Deployment to load the new value.

```sh
helm lint ./helm
helm template telemetry-ui ./helm -f ./helm/examples/unified.yaml
helm template telemetry-ui ./helm -f ./helm/examples/split.yaml -f ./helm/examples/gateway.yaml
helm template telemetry-ui ./helm -f ./helm/examples/catalog.yaml
```
