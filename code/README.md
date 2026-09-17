# Application and platform code

This directory keeps application code and infrastructure together to simplify
the local demo. The monorepo makes it easy to build a service, deploy it through
Argo CD, and explore the platform from one checkout. At scale, application
source belongs in separate repositories so teams can develop, test, and
release their services independently.

## Directory roles

| Directory | Responsibility |
| --- | --- |
| `apps/` | Service source code, application tests, Dockerfiles, and local image tasks. |
| `infrastructure/` | Local cluster bootstrap, shared platform services, cluster policies, reusable workload charts, and GitOps deployment configuration. |

```text
code/
  apps/
    Taskfile.yml                 local application build/import tasks
    go-demo/                     example service, tests, and Dockerfile
  infrastructure/
    Taskfile.yml                 cluster lifecycle and operations
    scripts/                     local k3d bootstrap
    argo-apps/platform/           Argo CD, networking, observability, and Kyverno
    argo-apps/dev/                development app registration and Helm values
    charts/                      reusable deployment and platform charts
    tests/                       infrastructure policy tests
```

`apps/` defines what a service does and how its container is built.
`infrastructure/` defines where it runs and how it connects to the platform.
For example, Go demo source lives in `apps/go-demo/`, while its image reference,
probes, and HTTPRoute settings live in `infrastructure/argo-apps/dev/go-demo/`.
Argo CD reads the committed deployment configuration from Git.

## Local workflow

Windows uses Git Bash; macOS uses your normal Terminal and a running Linux
Docker engine. `task setup` selects winget or Homebrew for CLI installation.
See the [OS setup instructions](infrastructure/README.md#tools-and-first-run).
Optional `infrastructure/settings.local.yml` settings and `Taskfile.local.yml` tasks
let each developer choose Docker connections, ports, and runtime commands
without editing shared files. Colima is the documented Mac example.

From `code/infrastructure`:

```bash
task up          # bootstrap the local cluster and print service links
task go-demo     # build, save, import, and restart the demo image
task status      # inspect pods, apps, resource usage, and sizing
```

The application task lives in `apps/Taskfile.yml` and is also included by the
infrastructure Taskfile. Local k3d image imports keep the demo simple without
requiring an image registry. See [infrastructure setup](infrastructure/README.md)
and [Go demo details](apps/go-demo/README.md).

## Repository model as the platform grows

Each service would move to its own application repository, containing its
source, tests, Dockerfile, and CI pipeline. The platform repository would keep
shared services, policies, charts, and environment deployment configuration.
Separating source repositories does not require moving each service's deployment
values out of the platform repository.

The delivery flow would become:

1. Application CI tests the service and publishes its container to a registry.
2. A deployment change updates the environment's image reference in the platform
   repository, using a unique release tag or digest.
3. Argo CD reconciles that Git change into the target cluster.

Clusters then pull images from the registry rather than using local imports.
The platform's Argo CD Applications reference deployment charts and values;
they do not need the application's source checkout. An application-owned chart
could instead be published separately and referenced by the platform.

This repository demonstrates the boundaries in one place. A production rollout
would also revisit the local defaults: single replicas, filesystem-backed
telemetry storage, and the audit policy against CPU/memory sizing. Repository
separation alone does not make the cluster highly available or production-ready.
