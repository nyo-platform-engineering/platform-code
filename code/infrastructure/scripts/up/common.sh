#!/usr/bin/env bash
# Shared environment and helpers for operational scripts and startup steps.
# Resolve relative paths from infrastructure/, regardless of the caller's directory.
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
root="$PWD"
namespace=sistem-informasi-kebencanaan-bnpb
# Use an explicit kubeconfig so commands do not select the host's default cluster.
export KUBECONFIG="${KUBECONFIG:-$root/.state/kubeconfig}"

kube() {
  kubectl --kubeconfig "$KUBECONFIG" "$@"
}

helmc() {
  helm --kubeconfig "$KUBECONFIG" "$@"
}

links() {
  local address="${AAT_NODE_ADDRESS:-${AAT_NETWORK_PREFIX:-192.168.56}.10}"
  local port="${GATEWAY_PORT:-80}"
  local suffix=""
  local service_name

  if [[ "$port" != 80 ]]; then
    suffix=":$port"
  fi

  printf 'Local gateway URLs (PORT=%s):\n' "$port"
  for service_name in client auth radar grafana argocd; do
    printf '  http://%s.localhost%s\n' "$service_name" "$suffix"
  done
  printf '  http://traefik.localhost%s/dashboard/\n' "$suffix"
  printf 'Direct VM access:\n'
  printf 'Client: http://%s:30080\n' "$address"
  printf 'Auth: http://%s:30084\n' "$address"
  printf 'Grafana: http://%s:30300 (admin / task password)\n' "$address"
  printf 'Argo CD: http://argocd.localhost%s (admin / task password APP=argocd)\n' "$suffix"
}
