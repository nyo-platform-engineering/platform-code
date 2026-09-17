# GitLab wrapper

This chart depends on the official `gitlab/gitlab` chart. `Chart.lock` pins its
version; downloaded archives in `charts/` are ignored. Argo CD builds the
dependency from the upstream repository.

Settings live beside the Application at
`argo-apps/platform/03-cd/gitlab/values.yaml`. Shared `global` settings are at
the wrapper's root and propagate to dependencies. Component settings are under
`gitlab`, including the upstream chart's own `gitlab` component group.

Keep release name `gitlab` to preserve resource names and existing volumes.
When updating the upstream version in `Chart.yaml`, run `helm dependency update`
in this directory and commit the updated lock file.
