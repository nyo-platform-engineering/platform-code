#!/usr/bin/env bash
# Shared environment and helpers for operational scripts and startup steps.
# Resolve relative paths from infrastructure/, regardless of the caller's directory.
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
root="$PWD"
namespace=sistem-informasi-kebencanaan-bnpb
# Use an explicit kubeconfig so commands do not select the host's default cluster.
export KUBECONFIG="${KUBECONFIG:-$root/.state/kubeconfig}"

# Lab mode: "vm" (default, three VMs) or "k3d" (task up -- --novm, development
# only). The mode is recorded in .state/mode when the cluster is created.
k3d_cluster="${AAT_K3D_CLUSTER:-aat}"
lab_mode=vm
if [[ "${AAT_NOVM:-}" == 1 || "$(cat "$root/.state/mode" 2>/dev/null)" == k3d ]]; then
  lab_mode=k3d
  export AAT_NOVM=1
  # NodePorts are published on the host's loopback by k3d.
  export AAT_NODE_ADDRESS="${AAT_NODE_ADDRESS:-127.0.0.1}"
fi

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
  if [[ "$lab_mode" == k3d ]]; then
    printf 'Direct NodePort access (k3d, development only):\n'
  else
    printf 'Direct VM access:\n'
  fi
  printf 'Client: http://%s:30080\n' "$address"
  printf 'Auth: http://%s:30084\n' "$address"
  printf 'Grafana: http://%s:30300 (admin / task password)\n' "$address"
  printf 'Argo CD: http://argocd.localhost%s (admin / task password)\n' "$suffix"
}
