# Go demo

A standard-library Go HTTP service with JSON request logs, health probes,
and graceful shutdown. No framework or external Go packages are required.

| Endpoint | Response |
| --- | --- |
| `GET /` | JSON greeting |
| `GET /healthz` | `ok` (liveness) |
| `GET /readyz` | `ok` (readiness; this app has no external dependencies) |

From this directory:

```bash
go test ./...
go run .
```

Browse `http://localhost:8080`. Set `PORT` to override the default 8080.

## Container and local cluster

```bash
docker build -t local/go-demo:dev1 .
docker run --rm -p 127.0.0.1:8080:8080 local/go-demo:dev1
```

The runtime image contains only the static Go binary and runs as a non-root
user. Deployment configuration lives under `code/infrastructure/argo-apps`,
using the shared `charts/deployment` chart. No Kubernetes manifests belong
in this application directory. Resource sizing is empty by default.

With the `dev` k3d cluster running, import the image before syncing Argo CD:

```bash
k3d image import local/go-demo:dev1 -c dev
```

The Application is registered in
`../../infrastructure/argo-apps/dev/go-demo/app.yaml`, with deployment settings
in `../../infrastructure/argo-apps/dev/go-demo/values.yaml`. It uses the
shared deployment chart, and the development root registers it through its
Kustomization. Push this configuration to Git and
rerun `task up` from `code/infrastructure` to register the renamed root path.
After sync, browse `http://go.localhost`. The HTTPRoute uses the platform's
Traefik Gateway in namespace `traefik`.

For updates, use a new image tag (for example `dev2`), update `image.tag` in
`argo-apps/dev/go-demo/values.yaml`, rebuild/import it, and push the change.
Keep the image tag and deployment settings aligned so nodes run the intended
build. See the [chart README](../../infrastructure/charts/deployment/README.md)
for local rendering and configuration options.
