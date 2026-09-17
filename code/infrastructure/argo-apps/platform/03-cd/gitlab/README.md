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
from local app code if it changes, triggering CI. Use normal Git pushes for
other repository changes. `ACTION=projects` retries just the import.
The importer creates a temporary API token inside toolbox and revokes it on exit.
The upstream Kubernetes runner, registry, KAS, and exporter are enabled.
`task gitlab` provisions the runner's authentication token into a Kubernetes
Secret. Jobs run without privileged containers, with one concurrent job.
Runner API and clone URLs use cluster DNS so jobs do not resolve `.localhost`
to their own loopback interface. Go demo's pipeline runs tests, `go vet`, and
a Linux binary build, retaining the binary as a one-day artifact.

The registry uses the existing MinIO instance and appears at
`http://registry.localhost`; KAS is available at `ws://kas.localhost`.
Use `task links` for port-aware URLs. The local registry uses HTTP; configure
your Docker runtime to trust it as an insecure registry before Docker pushes.
GitLab's Prometheus and additional ingress controllers remain disabled because
the platform already supplies monitoring and Traefik. Exporter metrics are
available internally; no broad GitLab metric scrape is added.
HTTP Git access uses the existing Gateway; SSH is not exposed yet.

Dependency data, Git repositories, and app source persist in Kubernetes volumes.
Stopping k3d preserves them; deleting/recreating the cluster deletes local data.
