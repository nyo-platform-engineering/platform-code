# Go demo

This is the example app you can follow through the platform. It's a small Go
HTTP service with JSON request logs, health checks, and graceful shutdown.
It uses the Go standard library and has no external application dependencies.

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
| `GET /healthz` | `ok`: the process is alive. |
| `GET /readyz` | `ok`: the service is ready to receive requests. |

## Try the full CI deployment

From `code/infrastructure`, with your Docker runtime running:

```bash
task up
task gitlab
task links
```

Sign in to GitLab as `root` using `task password APP=gitlab`. Open
**root/go-demo** and its pipelines. The initial import starts the pipeline.

| Stage | What happens |
| --- | --- |
| Lint | Check formatting with `gofmt` and run `go vet`. |
| Test | Run `go test ./...`. |
| Build | Compile a static binary and publish a scratch image to GitLab's registry using Crane. |
| Deploy | On `main`, commit the image digest to `deploy/values.yaml` and wait for Argo CD's rollout. |

Build jobs need no Docker socket or privileged container. They build for the
runner node's architecture and retain the binary and image reference as
one-day artifacts. Deploy uses a short-lived CI job token to push the values
change; that push doesn't start another pipeline.

Use **normal Git pushes to the local GitLab project** for subsequent app
changes. Running `task gitlab` again updates CI configuration and preserves
existing source, history, and the image chosen by CI. Uploading files into the
toolbox volume alone does not update existing application source in GitLab.

After deploy succeeds, open Go demo using `task links`. For the default ports,
it's at `http://go.localhost`.

## Build locally instead

With the cluster and GitLab project configured, run from `code/apps` or
`code/infrastructure`:

```bash
task go-demo
# Optional: task go-demo IMAGE_TAG=dev2
```

This one command builds the image (including tests), saves a local archive,
imports it into k3d, selects it in the GitLab deployment values, restarts the
Deployment, and waits for readiness. The default image is `local/go-demo:dev1`
and the archive is `code/apps/go-demo/go-demo-dev1.tar`. Archives are ignored
by Git and excluded from the Docker build context.

The task follows the cluster node's architecture, including arm64 on Apple
Silicon. Use infrastructure settings for a different cluster or Docker context.
The next successful `main` pipeline selects its registry image again.

To try the container by itself, from this app directory:

```bash
docker build -t local/go-demo:dev1 .
docker run --rm -p 127.0.0.1:8080:8080 local/go-demo:dev1
```

The runtime image contains only the static binary and runs as a non-root user.

## Where deployment settings live

| File | Responsibility |
| --- | --- |
| [`.gitlab-ci.yml`](.gitlab-ci.yml) | App lint, test, image build, and deployment pipeline. |
| [`deploy/values.yaml`](deploy/values.yaml) | Initial image override; CI updates the copy in the local GitLab project. |
| [Infrastructure Application](../../infrastructure/argo-apps/dev/go-demo/app.yaml) | Tell Argo CD which chart and Git repositories to read. |
| [Base deployment values](../../infrastructure/argo-apps/dev/go-demo/values.yaml) | Probes, security settings, ports, and the `go.localhost` route. |

Argo CD reads the Helm chart and base values from GitHub, then the image
override from GitLab. Local checkout edits need to be pushed to the repository
Argo CD reads. The HTTPRoute attaches to the platform's Traefik Gateway.

Continue with the [GitLab guide](../../infrastructure/argo-apps/platform/03-cd/gitlab/README.md)
or the [deployment chart guide](../../infrastructure/charts/deployment/README.md).
