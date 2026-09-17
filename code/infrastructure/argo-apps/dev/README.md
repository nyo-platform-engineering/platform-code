# Development apps

This folder tells Argo CD which applications to run in the development
namespace, `dev`. Application source and Dockerfiles live in `code/apps`;
these files describe how those apps are deployed.

For a first run, use the [repository guide](../../../../README.md).
For cluster setup, use the [infrastructure guide](../../README.md).

## Understand the files

Each app has a folder with two main files:

| File | What it does |
| --- | --- |
| `app.yaml` | Register an Argo CD Application and choose its chart, Git sources, and destination. |
| `values.yaml` | Configure the workload: image, probes, security, Service, and route. |

[`kustomization.yaml`](kustomization.yaml) lists the Application manifests to
register. Helm values aren't Kubernetes resources, so don't add them to that
list. The `dev-apps` parent registers this layer at sync wave 5.

## Follow Go demo

[`go-demo/app.yaml`](go-demo/app.yaml) uses the shared
[deployment chart](../../charts/deployment/README.md). It reads
[`go-demo/values.yaml`](go-demo/values.yaml) for base deployment settings and
`deploy/values.yaml` from the local GitLab **root/go-demo** project for the
image override.

From `code/infrastructure`, run `task gitlab` to import the project and configure
repository and registry access. CI updates the GitLab override with its built
image digest; Argo CD rolls it out. `task go-demo` offers a local build/import
and selects that local image in the same override.

See the [app guide](../../../apps/go-demo/README.md) for both workflows.

## Add another app

1. Create `argo-apps/dev/<app>/` and copy
   [`examples/application.yaml`](../../examples/application.yaml) into `app.yaml`.
2. Set a unique Application name, release name, repository URL, and chart path.
   Keep the Application itself in `argocd` and choose its workload destination.
3. Create `values.yaml` with an available image and the app's deployment settings.
   Point the Application's Helm value file reference to it.
4. Add `<app>/app.yaml` to `kustomization.yaml`.
5. Commit and push to the repository Argo CD reads, then inspect the new
   Application's sync and health status.

Nodes must be able to pull the image, or have it imported locally, before the
workload can start. Private registries also need an image pull Secret.

The parent can show Healthy while an individual child is still starting or
unhealthy. Check the child's Application to understand its actual rollout.
