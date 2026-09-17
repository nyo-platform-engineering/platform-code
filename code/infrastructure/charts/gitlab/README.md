# GitLab chart wrapper

This small chart lets us keep local GitLab integration in the repo while using
the official GitLab Helm chart for GitLab itself. It contains a dependency,
not a separate implementation of GitLab.

For setup, passwords, projects, and CI, use the
[GitLab guide](../../argo-apps/platform/03-cd/gitlab/README.md).
This page is for changing the chart dependency or its values.

## Find the configuration

| File | What it controls |
| --- | --- |
| [`Chart.yaml`](Chart.yaml) | The official chart dependency and requested version. |
| [`Chart.lock`](Chart.lock) | The resolved dependency version and checksum. |
| [`values.yaml`](values.yaml) | Wrapper defaults. |
| [Application values](../../argo-apps/platform/03-cd/gitlab/values.yaml) | This platform's GitLab settings. |

Argo CD downloads the pinned dependency when rendering this chart. Downloaded
archives in `charts/` are ignored by Git.

## Understand the nesting

Shared `global` settings belong at the wrapper's root. GitLab component values
go under the dependency name, `gitlab`. The upstream chart also has a component
group named `gitlab`, so some settings have two levels:

```yaml
global:
  edition: ce
gitlab:
  gitlab-runner:
    install: true
  gitlab:
    webservice:
      workerProcesses: 1
```

Keep the release name **gitlab** to preserve resource names and existing volumes.

## Update the upstream chart

Edit the dependency version in `Chart.yaml`, then run from this directory:

```bash
helm dependency update
helm lint . -f ../../argo-apps/platform/03-cd/gitlab/values.yaml
```

Commit both `Chart.yaml` and `Chart.lock` and push the change. Argo CD handles
the rollout. Check the GitLab Application and `task status` afterward: chart
upgrades can change defaults, including CPU/memory settings and components.

[Back to infrastructure](../../README.md) ? [Repository guide](../../../../README.md)
