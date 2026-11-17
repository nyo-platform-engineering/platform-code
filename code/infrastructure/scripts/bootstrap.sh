#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
root="$PWD"
namespace=sistem-informasi-kebencanaan-bnpb
export KUBECONFIG="${KUBECONFIG:-$root/.state/kubeconfig}"
kube() { kubectl --kubeconfig "$KUBECONFIG" "$@"; }
helmc() { helm --kubeconfig "$KUBECONFIG" "$@"; }
doctor() {
  for tool in kubectl helm python3; do
    command -v "$tool" >/dev/null || { echo "Missing $tool; see README.md." >&2; return 1; }
  done
}
prepare() {
  doctor
  [[ -f "$KUBECONFIG" ]] || { echo 'Kubeconfig missing. Run task up or set KUBECONFIG to your local VM cluster.' >&2; return 1; }
  kube wait --for=condition=Ready nodes --all --timeout=180s
  kube get nodes -o json | python3 scripts/topology.py
  mkdir -p .state
  chmod 700 .state
  for ns in "$namespace" monitoring argocd; do
    kube create namespace "$ns" --dry-run=client -o yaml | kube apply -f -
  done
  python3 scripts/runtime.py
}
bootstrap_argocd() {
  local existing version
  existing="$(kube get deployment argocd-server -n argocd --ignore-not-found -o name)"
  if [[ -z "$existing" ]]; then
    version="$(sed -n '/chart: argo-cd/{n;s/.*targetRevision: *//p;}' argo-apps/platform/01-cd/argocd/app.yaml | tr -d '\r\047\042')"
    [[ -n "$version" ]] || { echo 'Cannot read the pinned Argo CD chart version.' >&2; return 1; }
    helmc upgrade --install argocd argo-cd --repo https://argoproj.github.io/argo-helm \
      --version "$version" -n argocd \
      -f argo-apps/platform/01-cd/argocd/values.yaml --wait --timeout 10m
  fi
  # Only seed a missing controller. Argo CD owns all subsequent reconciliation.
  kube rollout status deployment/argocd-server -n argocd --timeout=300s
  kube rollout status deployment/argocd-repo-server -n argocd --timeout=300s
  kube rollout status statefulset/argocd-application-controller -n argocd --timeout=300s
}
deploy() {
  prepare
  bootstrap_argocd
  kube apply -f argo-apps/root.yaml
  echo 'Root Application registered. Argo CD reconciles platform and AAT from Git asynchronously; use task status.'
  links
}
links() {
  local address="${AAT_NODE_ADDRESS:-${AAT_NETWORK_PREFIX:-192.168.56}.10}"
  local port="${GATEWAY_PORT:-80}" suffix=""
  if [[ "$port" != 80 ]]; then suffix=":$port"; fi
  printf 'With task gateway running (PORT=%s):\n' "$port"
  printf '  http://%s.localhost%s\n' client "$suffix" auth "$suffix" radar "$suffix" grafana "$suffix" argocd "$suffix"
  printf '  http://traefik.localhost%s/dashboard/\n' "$suffix"
  printf 'Direct VM access:\n'
  printf 'Client: http://%s:30080\n' "$address"
  printf 'Auth: http://%s:30084\n' "$address"
  printf 'Grafana: http://%s:30300 (admin / task password)\n' "$address"
  printf 'Argo CD: task argocd:ui (admin / task password APP=argocd)\n'
}

gateway() {
  local port="${1:-80}" kubectl_bin
  if [[ ! "$port" =~ ^[1-9][0-9]{0,4}$ ]] || (( port > 65535 )); then
    echo 'Gateway PORT must be 1..65535.' >&2
    return 2
  fi
  if [[ ! -f "$KUBECONFIG" ]]; then
    echo 'Kubeconfig missing. Run task up first.' >&2
    return 1
  fi
  kubectl_bin="$(command -v kubectl)"
  kube rollout status deployment/traefik -n traefik --timeout=300s
  kube wait --for=condition=Programmed gateway/local -n traefik --timeout=180s
  GATEWAY_PORT="$port" links
  echo 'Keep this terminal open; Ctrl+C stops local gateway access.'
  # Only the loopback listener needs elevated privileges for a low port.
  # Pass the explicit lab kubeconfig even when sudo changes the environment.
  if (( port < 1024 && EUID != 0 )); then
    echo 'Binding a port below 1024 uses sudo. Use PORT=8080 to avoid sudo.'
    exec sudo "$kubectl_bin" --kubeconfig "$KUBECONFIG" port-forward \
      --address localhost -n traefik service/traefik "$port:80"
  fi
  exec "$kubectl_bin" --kubeconfig "$KUBECONFIG" port-forward \
    --address localhost -n traefik service/traefik "$port:80"
}

case "${1:-help}" in
  doctor) doctor; bash scripts/vm.sh doctor ;;
  up) doctor; export KUBECONFIG="$root/.state/kubeconfig"; bash scripts/vm.sh up; deploy ;;
  deploy) deploy ;;
  prepare) prepare ;;
  images)
    command -v docker >/dev/null || { echo 'docker is needed for task images.' >&2; exit 1; }
    python3 scripts/images.py | while IFS= read -r image; do docker pull "$image"; done
    ;;
  status)
    kube get nodes -o wide
    kube get applications -n argocd
    kube get pods,services,statefulsets,hpa,pdb -n "$namespace" -o wide
    kube get pods -n monitoring -o wide
    kube get pods,services -n traefik -o wide
    kube get pods,services -n radar -o wide
    ;;
  links)
    links
    ;;
  gateway)
    gateway "${2:-80}"
    ;;
  password)
    case "${2:-grafana}" in
      grafana) cat .state/grafana-password ;;
      argocd) kube get secret argocd-initial-admin-secret -n argocd -o go-template='{{.data.password | base64decode}}{{"\n"}}' ;;
      *) echo 'Choose grafana or argocd.' >&2; exit 2 ;;
    esac
    ;;
  argocd-ui) kube port-forward -n argocd svc/argocd-server 8088:80 ;;
  tester-install)
    kube apply -f https://raw.githubusercontent.com/susTuna/if4031-tester-agent/ebb55261711123a9a211739ffbf140a275caa6da/manifest.yaml
    ;;
  tester-list) kube exec -n tester-agent daemonset/tester-agent -- /app list ;;
  tester-practice)
    [[ -n "${2:-}" ]] || { echo 'Pass the scenario name.' >&2; exit 2; }
    kube exec -n tester-agent daemonset/tester-agent -- /app run practice "$2"
    ;;
  scale)
    [[ "${2:-}" =~ ^[2-5]$ ]] || { echo 'Replica count must be 2..5.' >&2; exit 2; }
    [[ -z "$(kube get hpa aggregator -n "$namespace" --ignore-not-found -o name)" ]] || { echo 'Disable Aggregator HPA before manual scale tests.' >&2; exit 1; }
    kube scale deployment/aggregator -n "$namespace" --replicas="$2"
    kube rollout status deployment/aggregator -n "$namespace" --timeout=180s
    ;;
  logs)
    kube logs -n "$namespace" -l "app.kubernetes.io/instance=${2:-aggregator}" \
      --all-containers --tail=100 --follow
    ;;
  *)
    echo 'task --list | task up | task deploy | task status | task gateway | task links'
    ;;
esac
