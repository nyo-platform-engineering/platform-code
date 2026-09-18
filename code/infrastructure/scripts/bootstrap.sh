#!/usr/bin/env bash
# Bash 3.2+ on macOS, Linux, or Windows Git Bash, with a Linux Docker engine.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export MSYS_NO_PATHCONV=1
name="${CLUSTER_NAME:-dev}"
context="k3d-$name"
[[ "$name" =~ ^[a-z][a-z0-9-]{0,40}$ ]] || { echo 'Invalid CLUSTER_NAME' >&2; exit 1; }
kube() { kubectl --context "$context" "$@"; }
doctor() {
  for tool in docker kubectl helm k3d; do
    command -v "$tool" >/dev/null || { echo "Missing $tool on PATH; see README.md for your OS setup." >&2; exit 1; }
  done
  local engine
  engine="$(docker info --format '{{.OSType}}')" || {
    echo 'Cannot reach Docker. Start your container runtime and check docker context ls / DOCKER_HOST.' >&2
    return 1
  }
  [[ "$engine" == linux ]] || { echo 'A Linux Docker engine is required; Rancher Desktop must use dockerd (Moby).' >&2; exit 1; }
  echo "Docker engine: $(docker info --format '{{.OSType}}/{{.Architecture}}')"
}
resources() {
  local sizing
  sizing="$(kube get pods -A -o go-template='{{range .items}}{{$pod := .}}{{range .spec.containers}}{{if or .resources.requests.cpu .resources.requests.memory .resources.limits.cpu .resources.limits.memory}}{{$pod.metadata.namespace}}/{{$pod.metadata.name}} {{.name}}: {{.resources}}{{"\n"}}{{end}}{{end}}{{range .spec.initContainers}}{{if or .resources.requests.cpu .resources.requests.memory .resources.limits.cpu .resources.limits.memory}}{{$pod.metadata.namespace}}/{{$pod.metadata.name}} {{.name}}: {{.resources}}{{"\n"}}{{end}}{{end}}{{end}}')"
  [[ -z "$sizing" ]] || { printf 'CPU/memory sizing remains:\n%s\n' "$sizing" >&2; return 1; }
  echo 'No CPU/memory requests or limits on cluster containers.'
}
clear_system_resources() {
  local workload kind container workloads containers
  workloads="$(kube get deployment,daemonset,statefulset -n kube-system -o name)"
  while IFS= read -r workload; do
    workload="${workload%$'\r'}"
    [[ -n "$workload" ]] || continue
    for kind in containers initContainers; do
      containers="$(kube get "$workload" -n kube-system -o "jsonpath={range .spec.template.spec.$kind[*]}{.name}{'\n'}{end}")"
      while IFS= read -r container; do
        container="${container%$'\r'}"
        [[ -n "$container" ]] || continue
        kube patch "$workload" -n kube-system --type=strategic -p \
          "{\"spec\":{\"template\":{\"spec\":{\"$kind\":[{\"name\":\"$container\",\"resources\":{\"requests\":{\"cpu\":null,\"memory\":null},\"limits\":{\"cpu\":null,\"memory\":null}}}]}}}}"
      done <<< "$containers"
    done
    kube rollout status "$workload" -n kube-system --timeout=180s
  done <<< "$workloads"
}
up() {
  doctor
  if k3d cluster list --no-headers | awk '{print $1}' | grep -Fxq "$name"; then
    k3d cluster start "$name"
  else
    k3d cluster create "$name" --servers 1 --agents 0 \
      --image "${K3S_IMAGE:-rancher/k3s:v1.34.5-k3s1}" \
      --api-port "127.0.0.1:${API_PORT:-6550}" \
      -p "127.0.0.1:${HTTP_PORT:-80}:80@loadbalancer" \
      -p "127.0.0.1:${HTTPS_PORT:-443}:443@loadbalancer" \
      --k3s-arg '--disable=traefik@server:0' --wait --timeout 180s
  fi
  k3d kubeconfig merge "$name" --kubeconfig-merge-default --kubeconfig-switch-context
  kube wait --for=condition=Ready nodes --all --timeout=180s
  kube apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION:-v1.5.1}/standard-install.yaml"
  for crd in gatewayclasses gateways httproutes tlsroutes; do
    kube wait --for=condition=Established "crd/$crd.gateway.networking.k8s.io" --timeout=60s
  done
  kube create namespace argocd --dry-run=client -o yaml | kube apply -f -
  # Only bootstrap a new installation. Argo CD owns subsequent configuration changes.
  if ! kube get deployment argocd-server -n argocd >/dev/null 2>&1; then
    helm repo add argo https://argoproj.github.io/argo-helm --force-update
    helm repo update argo
    version="$(sed -n '/chart: argo-cd/{n;s/.*targetRevision: *//p;}' argo-apps/platform/01-cd/argocd/app.yaml | tr -d '\r\047\042')"
    [[ -n "$version" ]] || { echo 'Cannot read Argo CD chart version.' >&2; exit 1; }
    helm template argocd argo/argo-cd --namespace argocd --version "$version" \
      --include-crds --values argo-apps/platform/01-cd/argocd/values.yaml | kube apply --server-side -f -
  fi
  kube rollout status deployment/argocd-server -n argocd --timeout=300s
  kube rollout status deployment/argocd-repo-server -n argocd --timeout=300s
  kube rollout status statefulset/argocd-application-controller -n argocd --timeout=300s
  kube create namespace dev --dry-run=client -o yaml | kube apply -f -
  bash scripts/gitlab.sh prepare
  clear_system_resources
  kube apply -f argo-apps/root.yaml
  echo 'Argo CD now reconciles argo-apps/platform and argo-apps/dev from Git. Commit/push these files to main first.'
  echo 'Use task status to monitor deployment and check live sizing.'
}
case "${1:-help}" in
  doctor) doctor ;;
  up) up ;;
  password)
    app="${2:-all}"
    case "$app" in
      all)
        status=0
        for app in argocd gitlab; do
          case "$app" in
            argocd) printf '== Argo CD ==\nusername:\nadmin\npassword:\n' ;;
            gitlab) printf '\n== GitLab ==\nusername:\nroot\npassword:\n' ;;
          esac
          bash scripts/bootstrap.sh password "$app" || status=1
        done
        exit "$status"
        ;;
      argocd) namespace=argocd; secret=argocd-initial-admin-secret ;;
      gitlab) namespace=gitlab; secret=gitlab-gitlab-initial-root-password ;;
      *) echo 'Supported password apps: all, argocd, gitlab' >&2; exit 1 ;;
    esac
    kube get secret "$secret" -n "$namespace" -o go-template='{{.data.password | base64decode}}{{"\n"}}'
    ;;
  resources) resources ;;
  help) echo 'task --list | task up | task status | task links | task password | task logs | task down' ;;
  *) echo "Unknown command: $1" >&2; exit 1 ;;
esac
