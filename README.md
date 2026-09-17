# Local platform playground

Run a small Kubernetes platform on your laptop and follow an app from source
code to a running service. This repo brings together a Go demo, GitLab CI,
Argo CD, networking, observability, and cluster policies.

Everything lives in one repo to make the demo easier to explore. In a larger
setup, application teams would have their own repositories and the platform
would keep shared infrastructure separately.

## What's here?

| Location | What you'll find |
| --- | --- |
| [code/apps](code/apps) | Application source, tests, Dockerfiles, CI pipelines, and local build tasks. Start with [Go demo](code/apps/go-demo/README.md). |
| [code/infrastructure](code/infrastructure/README.md) | Cluster setup, platform services, reusable Helm charts, and Argo CD deployment configuration. |
| [code/README.md](code/README.md) | How application and infrastructure responsibilities fit together, including future repo separation. |
| `docs/` | Separate documents; setup and component guides generally live beside their code. |

The stack uses **k3d** to run Kubernetes inside your Linux Docker engine.
**Traefik** gives services local URLs, **Argo CD** applies deployment changes
from Git, and **GitLab** hosts local app projects and runs their pipelines.
**Grafana, Mimir, Loki, and Tempo** provide dashboards and telemetry storage.
Two **OpenTelemetry Collectors** collect a small set of cluster metrics and
node/container telemetry. **Kyverno** reports cluster policy violations in
Audit mode.

## Get it running

Use **Git Bash on Windows** or your usual **Terminal on macOS**.
Start a Linux Docker engine first: Rancher Desktop in dockerd/Moby mode,
Docker Desktop, or Colima. This repo creates its own Kubernetes cluster;
disable the runtime's bundled Kubernetes to avoid port conflicts.

You'll need Git, Docker CLI, kubectl, Helm, k3d, and **Task 3.44 or newer**.
The [OS setup guide](code/infrastructure/README.md#tools-and-first-run)
includes installation instructions and a Colima example.

From the repository root:

```bash
cd code/infrastructure
task setup       # install supporting CLI tools on Windows or macOS
task up          # create the cluster and let Argo CD start the platform
task gitlab      # import apps, configure CI/registry access, and start the demo pipeline
task links       # show service URLs and login instructions
task status      # inspect startup and app health
```

Install Task before running these commands. On Windows, reopen Git Bash after
installing tools so the new commands are on PATH. The first startup takes time
to download images, start GitLab, and run database migrations.

**Argo CD reads infrastructure from GitHub**, using the repository URLs in
the Application manifests. Push your changes before expecting Argo CD to
apply them. If you're using a fork, update those URLs to your own repository.

### Pick your runtime and ports

The shared files don't need to change for each developer's machine. From
`code/infrastructure`, optionally copy the settings example:

```bash
cp settings.local.example.yml settings.local.yml
```

Set your Docker context, cluster name, or ports there. The example uses HTTP
port `8080`, so URLs become `http://gitlab.localhost:8080`, for example.
`task links` prints URLs for your settings. `settings.local.yml` is ignored by
Git; [personal settings and extensions](code/infrastructure/README.md#personal-settings-and-extensions)
also explain how to add your own runtime tasks.

## Try the app delivery flow

1. Open GitLab using `task links`. Sign in as `root`; get the password with
   `task password APP=gitlab`.
2. Open the imported **root/go-demo** project and its pipelines. The initial
   import starts **lint → test → build → deploy** automatically.
3. Build publishes a container image to GitLab's registry. On `main`, deploy
   commits its image digest to `deploy/values.yaml` in the GitLab app project.
4. Argo CD combines that file with this repo's Helm chart and base settings,
   rolls out the app, and CI waits for the Deployment to become ready.
5. Open **Go demo** from `task links`. Push a code change to the GitLab project
   to run the flow again.

App development happens in the **local GitLab project** after the initial
import. Infrastructure changes happen in **this GitHub repo**. Running
`task gitlab` again updates pipeline configuration and preserves existing
app source, history, and the image selected by CI. It does not synchronize
every subsequent local source edit into GitLab.

For a local build without publishing an image, run `task go-demo`. It builds,
saves, and imports the image into k3d, then selects it in the GitOps values.
The next successful `main` pipeline selects its registry image again.
See the [Go demo guide](code/apps/go-demo/README.md) and
[GitLab guide](code/infrastructure/argo-apps/platform/03-cd/gitlab/README.md)
for details.

## Everyday commands

Run these from `code/infrastructure`:

| Command | Use it to |
| --- | --- |
| `task` | List available tasks. |
| `task links` | Find service URLs and login details. |
| `task password` | Get the Argo CD `admin` password. |
| `task password APP=gitlab` | Get GitLab's initial `root` password. |
| `task status` | Inspect pods and each Argo CD Application. |
| `task logs APP=go-demo NAMESPACE=dev` | Follow Go demo logs. |
| `task gitlab ACTION=status` | Inspect GitLab startup. |
| `k3d cluster stop dev` | Pause the default cluster while keeping its data. |
| `task up` | Start an existing cluster or bootstrap a new one. |
| `task down` | Delete the cluster **and its local data**. |

Use your configured cluster name instead of `dev` when stopping it. GitLab
repositories and other state live in local Kubernetes volumes, so stopping
preserves them and deleting the cluster removes them.

If an app isn't ready, inspect its own Argo CD Application: the parent
`platform` or `dev-apps` can show Healthy while a child is still starting or
unhealthy. If Docker is unreachable, start your runtime and check
`docker context ls` and `docker info` first.

This is a local learning environment with single-instance services and local
storage. For component configuration and operating details, continue with the
[infrastructure guide](code/infrastructure/README.md).
