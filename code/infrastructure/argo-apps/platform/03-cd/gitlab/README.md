# Local GitLab

GitLab CE is deployed by Argo CD using a thin local chart in `charts/gitlab`
that depends on the official GitLab Helm chart, pinned with `Chart.lock`. Values
live here, beside its Application. `02-cd/gitlab-services` supplies local,
single-instance PostgreSQL, Redis, MinIO, and persistent volumes; these services
are separate because GitLab chart 10 no longer bundles them.

From `code/infrastructure`, run:

```bash
task gitlab                     # upload apps/ and import them as GitLab projects
task gitlab ACTION=status       # inspect GitLab startup
task password APP=gitlab        # initial root password (default APP is argocd)
```

Open `http://gitlab.localhost` (with your configured HTTP port if needed), or
run `task links`. Sign in as `root`. First deployment pulls several images and
runs database migrations, so the page may take several minutes to become ready.
Credentials are generated locally in Kubernetes Secrets and never committed.
`task up` also prepares the dependency credentials on a new cluster.

The toolbox mounts the app-code PVC **read-only** at `/workspace/apps`.
`task gitlab` copies the existing `code/apps` source into that volume using a
temporary writer pod. No host-path bind mount or cluster recreation is needed;
the same upload works with Windows, macOS/Colima, and other Docker runtimes.
Archives and `.git` directories are excluded. Re-run `task gitlab ACTION=code`
after changing sources; the upload overlays files and does not remove old ones.
Set `APP_CODE_DIR` in `settings.local.yml`, your shell, or Task arguments to
upload another checkout. Relative paths resolve from `code/infrastructure`.

After uploading, `task gitlab` imports each app directory into a private project
under `root`, with an initial `main` commit. Go demo appears at
`http://gitlab.localhost/root/go-demo` when signed in as `root`.
An existing project's source and history are preserved. Subsequent uploads
update mounted files; the importer also creates or updates `.gitlab-ci.yml`
from local app code if it changes, triggering CI. It seeds `deploy/values.yaml`
if missing and preserves the image CI selects thereafter. Use normal Git pushes for
other repository changes. `ACTION=projects` retries just the import.
The importer creates a temporary API token inside toolbox and revokes it on exit.
The upstream Kubernetes runner, registry, KAS, and exporter are enabled.
`task gitlab` provisions the runner's authentication token into a Kubernetes
Secret. Jobs run without privileged containers, with one concurrent job.
Runner API and clone URLs use cluster DNS so jobs do not resolve `.localhost`
to their own loopback interface. Cluster DNS routes `gitlab.localhost` and
`registry.localhost` through Traefik for registry authentication.

Go demo runs **lint → test → build → deploy**. Lint checks `gofmt` and `go vet`;
test runs Go tests. Build packages the static binary as a scratch image using
pinned Crane, without Docker sockets or privileged jobs, and publishes a
commit-tagged image to the GitLab registry. The binary and image reference are
retained as one-day artifacts. Images follow the runner node's architecture.

On `main`, deploy updates `deploy/values.yaml` in the **GitLab app repository**
with the immutable image digest. Argo CD combines this file with the deployment
chart and base values in GitHub. CI uses its short-lived job token to push to
its own project; these commits do not start another pipeline. Older source
revisions cannot overwrite a newer source push, and deployments are serialized.
The deploy job waits for the actual Deployment rollout. Its service account
can only read the `go-demo` Deployment in `dev`; it cannot modify workloads.
Repository and private-image credentials are project-scoped, read-only deploy
tokens, generated into Kubernetes Secrets. They have no expiry for this local
demo; revoke them in GitLab and delete both generated Secrets to reprovision.

The registry uses the existing MinIO instance and appears at
`http://registry.localhost`; KAS is available at `ws://kas.localhost`.
Use `task links` for port-aware URLs. The local registry uses HTTP; configure
your Docker runtime to trust it as an insecure registry before Docker pushes.
`task gitlab` configures containerd on every existing k3d node for this specific
HTTP registry without restarting nodes. Rerun it after adding nodes. Its node
host aliases route registry and GitLab authentication to node-local Traefik;
this does not depend on the host OS, Docker runtime, or mapped host HTTP port.
The HTTP registry and credentials are intended for the local development
cluster. Use HTTPS and managed credential rotation for remote environments.
GitLab's Prometheus and additional ingress controllers remain disabled because
the platform already supplies monitoring and Traefik. Exporter metrics are
available internally; no broad GitLab metric scrape is added.
HTTP Git access uses the existing Gateway; SSH is not exposed yet.

Dependency data, Git repositories, and app source persist in Kubernetes volumes.
Stopping k3d preserves them; deleting/recreating the cluster deletes local data.
