# Go demo

This is the example app you can follow through the platform. It's a small Go
HTTP service with JSON request logs, health checks, and graceful shutdown.
It uses the Go standard HTTP library and OpenTelemetry SDKs for trace/log export.

For live development with Signal Deck, follow the [local development guide](../dev/README.md).
Run `task dev:setup` and `task dev` from `code/apps`, then open
`http://127.0.0.1:8081/demo` to generate success, slow, and error traces with correlated logs.

New to the repo? Start with the [root guide](../../../README.md).

## Run it without Kubernetes

With Go installed, run these commands from `code/apps/go-demo`:

```bash
go test ./...
go run .
```

Open `http://localhost:8080`. Set `PORT` to use another port.

| Endpoint | What it returns |
| --- | --- |
| `GET /` | A JSON greeting. |
| `GET /demo` | Buttons for generating telemetry and links to traces. |
| `POST /demo/success` | Successful work and an info log. |
| `POST /demo/slow` | Approximately 500 ms work and a warning log. |
| `POST /demo/error` | Intentional HTTP 500 with error span/log. |
| `GET /healthz` | `ok`: the process is alive. |
| `GET /readyz` | `ok`: the service is ready to receive requests. |

## Build the container

From `code/apps`, run:

```bash
task go-demo-build
# Optional: task go-demo-build IMAGE_TAG=dev2
```

Or build and run directly from this app directory:

```bash
docker build -t local/go-demo:dev1 .
docker run --rm -p 127.0.0.1:8080:8080 local/go-demo:dev1
```

The runtime image contains only the static binary and runs as a non-root user.

## Deployment

The local infrastructure installs AAT and LGTM. Go demo is kept here for
standalone development and is not registered with infrastructure's Argo CD apps.
Use the [local development guide](../dev/README.md) to generate traces and
correlated logs in Signal Deck.

The app's `.gitlab-ci.yml` and `deploy/values.yaml` are retained for a separately
configured GitLab/Argo CD environment. Their deploy stage requires that environment's
registry credentials, Application, and scoped rollout access; the current
infrastructure bootstrap does not provision them.
