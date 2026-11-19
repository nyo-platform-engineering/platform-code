#!/usr/bin/env bash
# Development only (task up -- --novm): run the three k3s nodes as Docker
# containers with k3d instead of VMs. The nodes share the host kernel, so this
# cluster does NOT satisfy the demo's separate-kernel requirement; use it to
# develop and test on machines that cannot run the VM lab.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

state="$root/.state"
cluster="$k3d_cluster"
k3s_version="${K3S_VERSION:-v1.34.5+k3s1}"
# k3s image tags use "-" where k3s versions use "+".
image="${K3S_IMAGE:-rancher/k3s:${k3s_version/+/-}}"
api_port="${AAT_K3D_API_PORT:-6550}"
# NodePorts the lab exposes: Client API, Auth, Traefik gateway, Grafana.
node_ports=(30080 30084 30088 30300)

preflight_check() {
  for tool in k3d docker kubectl mktemp; do
    if ! command -v "$tool" >/dev/null; then
      echo "Missing $tool; see README.md (k3d mode)." >&2
      return 1
    fi
  done
  if ! docker info >/dev/null 2>&1; then
    echo 'Docker is not reachable. Start Docker Desktop (with WSL integration) or the Docker daemon.' >&2
    return 1
  fi
  # One state directory serves one lab; do not mix a VM cluster and a k3d cluster.
  if [[ -f "$state/mode" && "$(cat "$state/mode")" != k3d ]]; then
    echo 'This checkout already runs the VM lab. Run task destroy before switching to --novm.' >&2
    return 1
  fi
}

preflight_check

# Remaining steps (--check-only exits before running them):
# create_or_start_cluster - create the three named nodes, or start a stopped cluster.
# export_kubeconfig - write the cluster's kubeconfig to .state/kubeconfig only.
# wait_for_nodes - wait until all three nodes report Ready.

if [[ "${1:-}" == --check-only ]]; then
  echo 'k3d mode prerequisites present.'
  exit 0
fi

mkdir -p "$state"
chmod 700 "$state"

api_ready() {
  local kubeconfig
  kubeconfig="$(mktemp)"
  k3d kubeconfig get "$cluster" > "$kubeconfig" 2>/dev/null &&
    KUBECONFIG="$kubeconfig" kubectl get --raw /readyz >/dev/null 2>&1
  local ready=$?
  rm -f "$kubeconfig"
  return "$ready"
}

# After a stop/start, k3s inside the server container can exit at boot ("failed
# to find interface with specified node ip") while the container keeps running,
# and the load balancer may stay stopped. Restart the containers once, in order.
restart_stuck_nodes() {
  docker start "k3d-$cluster-serverlb" >/dev/null 2>&1 || true
  for _ in $(seq 1 30); do
    api_ready && return 0
    sleep 2
  done
  echo 'Kubernetes API did not come back after start; restarting the k3d node containers once.' >&2
  docker restart "k3d-$cluster-server-0" >/dev/null
  for _ in $(seq 1 60); do
    api_ready && break
    sleep 2
  done
  docker restart "k3d-$cluster-agent-0" "k3d-$cluster-agent-1" >/dev/null
  api_ready || { echo 'k3d API still unreachable; try task destroy and task up -- --novm.' >&2; exit 1; }
}

create_or_start_cluster() {
  if k3d cluster list "$cluster" >/dev/null 2>&1; then
    # An existing cluster keeps its nodes and local-path volumes across restarts.
    k3d cluster start "$cluster" --wait --timeout 300s || true
    restart_stuck_nodes
    return
  fi
  local ports=()
  for port in "${node_ports[@]}"; do
    ports+=(--port "127.0.0.1:${port}:${port}@server:0")
  done
  # Node names match the VM lab (aat-control, aat-worker-1, aat-worker-2), so the
  # same nodeSelectors and Citus placement apply unchanged. Like the VMs, k3s's own
  # Traefik and ServiceLB are disabled; Argo CD installs Traefik separately.
  k3d cluster create "$cluster" \
    --image "$image" \
    --servers 1 --agents 2 \
    --api-port "127.0.0.1:${api_port}" \
    --k3s-arg '--disable=traefik@server:0' \
    --k3s-arg '--disable=servicelb@server:0' \
    --k3s-arg '--node-name=aat-control@server:0' \
    --k3s-arg '--node-name=aat-worker-1@agent:0' \
    --k3s-arg '--node-name=aat-worker-2@agent:1' \
    "${ports[@]}" \
    --kubeconfig-update-default=false \
    --kubeconfig-switch-context=false \
    --wait --timeout 300s
}
create_or_start_cluster

export_kubeconfig() {
  umask 077
  local temporary
  temporary="$(mktemp "$state/kubeconfig.XXXXXX")"
  trap 'rm -f "$temporary"' EXIT
  k3d kubeconfig get "$cluster" > "$temporary"
  if [[ ! -s "$temporary" ]]; then
    echo 'k3d kubeconfig export failed.' >&2
    exit 1
  fi
  mv "$temporary" "$state/kubeconfig"
  trap - EXIT
  # Later steps and task commands read this marker to use k3d behaviour.
  echo k3d > "$state/mode"
}
export_kubeconfig

wait_for_nodes() {
  KUBECONFIG="$state/kubeconfig" kubectl wait --for=condition=Ready \
    node/aat-control node/aat-worker-1 node/aat-worker-2 --timeout=300s
}
wait_for_nodes

echo 'k3d cluster ready. Development only: nodes share one kernel and do not count for the demo.'
