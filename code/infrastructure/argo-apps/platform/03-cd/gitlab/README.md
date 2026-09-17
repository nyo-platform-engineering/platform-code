# Local GitLab

GitLab CE is deployed by Argo CD using the official GitLab Helm chart. Values
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
An existing project with commits is preserved; subsequent uploads update the
mounted files but do not overwrite GitLab history. Use normal Git pushes for
later repository changes. `ACTION=projects` retries just the import.
The importer creates a temporary API token inside toolbox and revokes it on exit.
The import commit skips CI. Runners, pipelines, registry, GitLab's Prometheus,
KAS, and additional ingress controllers are disabled. HTTP Git access uses the
platform's existing Traefik Gateway; SSH is not exposed yet.

Dependency data, Git repositories, and app source persist in Kubernetes volumes.
Stopping k3d preserves them; deleting/recreating the cluster deletes local data.
