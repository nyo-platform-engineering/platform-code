#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export MSYS_NO_PATHCONV=1
kube() { kubectl --context "k3d-${CLUSTER_NAME:-dev}" "$@"; }
prepare() {
  kube create namespace gitlab --dry-run=client -o yaml | kube apply -f -
  if ! kube get secret gitlab-local-auth -n gitlab >/dev/null 2>&1; then
    local password object_key
    password="$(openssl rand -hex 24)"
    object_key="$(openssl rand -hex 24)"
    kube create secret generic gitlab-local-auth -n gitlab \
      --from-literal=password="$password" --from-literal=accesskey=gitlab \
      --from-literal=secretkey="$object_key"
  fi
  local object_key
  object_key="$(kube get secret gitlab-local-auth -n gitlab -o go-template='{{index .data "secretkey" | base64decode}}')"
  if ! kube get secret gitlab-object-storage -n gitlab >/dev/null 2>&1; then
    kube create secret generic gitlab-object-storage -n gitlab --from-literal="connection=$(printf 'provider: AWS\nregion: us-east-1\naws_access_key_id: gitlab\naws_secret_access_key: %s\nendpoint: http://gitlab-minio:9000\npath_style: true\n' "$object_key")"
  fi
  if ! kube get secret gitlab-object-storage-backup -n gitlab >/dev/null 2>&1; then
    kube create secret generic gitlab-object-storage-backup -n gitlab --from-literal="config=$(printf '[default]\naccess_key = gitlab\nsecret_key = %s\nhost_base = gitlab-minio:9000\nhost_bucket = gitlab-minio:9000\nuse_https = False\n' "$object_key")"
  fi
  if ! kube get secret gitlab-registry-storage -n gitlab >/dev/null 2>&1; then
    kube create secret generic gitlab-registry-storage -n gitlab --from-literal="config=$(printf 's3:\n  accesskey: gitlab\n  secretkey: %s\n  bucket: registry\n  region: us-east-1\n  regionendpoint: http://gitlab-minio:9000\n  secure: false\n  v4auth: true\n  pathstyle: true\nredirect:\n  disable: true\n' "$object_key")"
  fi
}
code() {
  local source="${APP_CODE_DIR:-../apps}"
  test -d "$source" || { echo "App directory does not exist: $source" >&2; return 1; }
  upload_pod="gitlab-code-upload-$(date +%s)-$$"
  trap 'kube delete pod "$upload_pod" -n gitlab --ignore-not-found >/dev/null' EXIT
  cat <<EOF | kube create -f -
apiVersion: v1
kind: Pod
metadata:
  name: $upload_pod
  namespace: gitlab
spec:
  restartPolicy: Never
  containers:
  - name: upload
    image: alpine:3.23
    command: [sh, -c, 'sleep 3600']
    readinessProbe:
      exec:
        command: [sh, -c, 'test -d /workspace/apps']
    volumeMounts:
    - name: code
      mountPath: /workspace/apps
  volumes:
  - name: code
    persistentVolumeClaim:
      claimName: gitlab-app-code
EOF
  kube wait -n gitlab --for=condition=Ready "pod/$upload_pod" --timeout=180s
  tar --exclude='*.tar' --exclude='.git' -C "$source" -cf - . | \
    kube exec -i -n gitlab "$upload_pod" -- tar -xf - -C /workspace/apps
  kube delete pod "$upload_pod" -n gitlab --wait=false
  trap - EXIT
  echo 'App sources uploaded; toolbox mounts them read-only at /workspace/apps.'
}
projects() {
  kube rollout status deployment/gitlab-toolbox -n gitlab --timeout=300s
  kube rollout status deployment/gitlab-webservice-default -n gitlab --timeout=300s
  kube exec -i -n gitlab deployment/gitlab-toolbox -c toolbox -- gitlab-rails runner - < scripts/gitlab-import.rb
}
deploy() {
  kube create namespace dev --dry-run=client -o yaml | kube apply -f -
  if ! kube get secret go-demo-repository -n argocd >/dev/null 2>&1 || \
     ! kube get secret go-demo-registry -n dev >/dev/null 2>&1; then
    local credentials
    credentials="$(kube exec -i -n gitlab deployment/gitlab-toolbox -c toolbox -- gitlab-rails runner - < scripts/gitlab-deploy.rb)"
    # Toolbox may emit startup notices; the final line contains the manifest.
    printf '%s\n' "$credentials" | tail -n 1 | kube apply --server-side -f -
  fi
  bash scripts/gitlab-registry.sh
}
runner() {
  local configured output auth
  configured="$(kube get secret gitlab-gitlab-runner-secret -n gitlab -o go-template='{{if index .data "runner-token"}}yes{{end}}' 2>/dev/null || true)"
  if [[ "$configured" != yes ]]; then
    kube rollout status deployment/gitlab-toolbox -n gitlab --timeout=300s
    output="$(kube exec -i -n gitlab deployment/gitlab-toolbox -c toolbox -- gitlab-rails runner - < scripts/gitlab-runner.rb)"
    auth="$(printf '%s\n' "$output" | sed -n 's/^RUNNER_TOKEN://p')"
    [[ "$auth" == glrt-* ]] || { echo 'Could not obtain a runner authentication token.' >&2; return 1; }
    kube create secret generic gitlab-gitlab-runner-secret -n gitlab \
      --from-literal=runner-token="$auth" --from-literal=runner-registration-token='' \
      --dry-run=client -o yaml | kube apply --server-side --force-conflicts -f -
  fi
  echo 'Kubernetes runner authentication configured.'
}
ready() {
  local app="${1:-gitlab}" state
  kube annotate application "$app" -n argocd argocd.argoproj.io/refresh=hard --overwrite
  echo "Waiting for Argo CD to reconcile $app and finish its rollout..."
  for ((attempt=0; attempt<120; attempt++)); do
    state="$(kube get application "$app" -n argocd -o go-template='{{if index .metadata.annotations "argocd.argoproj.io/refresh"}}Refreshing{{else}}{{.status.sync.status}}/{{.status.health.status}}{{end}}')"
    if [[ "$state" == Synced/Healthy ]]; then
      return 0
    fi
    sleep 5
  done
  echo "$app not ready: $state; inspect task gitlab ACTION=status." >&2
  return 1
}
case "${1:-up}" in
  prepare) prepare ;;
  up)
    prepare
    kube apply --server-side -f argo-apps/platform/02-cd/gitlab-services/app.yaml
    kube apply --server-side -f argo-apps/platform/03-cd/gitlab/app.yaml
    echo 'Waiting for the GitLab app-code PVC...'
    for ((attempt=0; attempt<120; attempt++)); do
      kube get pvc gitlab-app-code -n gitlab >/dev/null 2>&1 && break
      sleep 5
    done
    code
    runner
    ready
    projects
    deploy
    echo 'Use task links and task gitlab ACTION=status to inspect GitLab startup.'
    ;;
  code) code ;;
  projects) projects ;;
  local-image)
    # Keep the registry-free task usable after switching the app to GitLab GitOps values.
    if kube get secret go-demo-repository -n argocd >/dev/null 2>&1; then
      kube exec -i -n gitlab deployment/gitlab-toolbox -c toolbox -- \
        env "LOCAL_IMAGE_TAG=${IMAGE_TAG:-dev1}" gitlab-rails runner - < scripts/gitlab-import.rb
      ready go-demo
    fi
    ;;
  runner) runner ;;
  status) kube get pods,pvc -n gitlab; kube get applications gitlab gitlab-services -n argocd ;;
  logs) kube logs -n gitlab -l app=webservice -c webservice --tail=100 --follow ;;
  *) echo 'Supported actions: up, prepare, code, projects, runner, status, logs; passwords: task password' >&2; exit 1 ;;
esac
