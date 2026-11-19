#!/usr/bin/env bash
# Manage Vagrant guests and export a kubeconfig for this lab only.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
root="$PWD"
state="$root/.state"

# k3d lab (task up -- --novm): the same commands act on Docker containers.
# Worker aat-worker-1/2 run in containers k3d-<cluster>-agent-0/1.
if [[ "$(cat "$state/mode" 2>/dev/null)" == k3d ]]; then
  cluster="${AAT_K3D_CLUSTER:-aat}"
  container() {
    case "$1" in
      aat-worker-1) echo "k3d-$cluster-agent-0" ;;
      aat-worker-2) echo "k3d-$cluster-agent-1" ;;
      *) echo 'Choose aat-worker-1 or aat-worker-2; the control plane must stay running during failure tests.' >&2; exit 2 ;;
    esac
  }
  case "${1:-help}" in
    doctor) bash scripts/up/1-k3d.sh --check-only ;;
    up) bash scripts/up/1-k3d.sh ;;
    status) k3d node list ;;
    halt)
      python3 scripts/up/gateway.py stop
      k3d cluster stop "$cluster"
      ;;
    resume) k3d cluster start "$cluster" --wait ;;
    destroy)
      k3d cluster delete "$cluster"
      python3 scripts/up/gateway.py stop
      rm -f "$state/mode" "$state/kubeconfig"
      ;;
    # Freezing a container is the k3d equivalent of suspending a VM.
    suspend-worker) docker pause "$(container "${2:-aat-worker-1}")" ;;
    resume-worker) docker unpause "$(container "${2:-aat-worker-1}")" ;;
    manager) echo 'k3d mode has no VMs; use docker ps or task k9s.' >&2; exit 2 ;;
    *) echo 'Usage (k3d): scripts/vm/main.sh doctor|up|status|halt|resume|destroy|suspend-worker|resume-worker [worker]' ;;
  esac
  exit
fi
vm() {
  # The subshell keeps Vagrant's working directory local to this command.
  (cd "$root/local" && vagrant "$@")
}

case "${1:-help}" in
  manager)
    if ! command -v virt-manager >/dev/null; then
      echo 'virt-manager is missing. Install it before running task vm:manager.' >&2
      exit 1
    fi
    exec virt-manager --connect qemu:///system
    ;;
  doctor)
    bash scripts/up/1-vms.sh --check-only
    ;;
  up)
    bash scripts/up/1-vms.sh
    ;;
  status)
    vm status
    ;;
  halt)
    python3 scripts/up/gateway.py stop
    vm halt
    ;;
  resume)
    vm resume
    ;;
  destroy)
    # Vagrant provides its own confirmation prompt; credentials remain for rebuilds.
    vm destroy
    python3 scripts/up/gateway.py stop
    ;;
  suspend-worker|resume-worker)
    node="${2:-aat-worker-1}"
    if [[ "$node" != aat-worker-1 && "$node" != aat-worker-2 ]]; then
      echo 'Choose aat-worker-1 or aat-worker-2; the control plane must stay running during failure tests.' >&2
      exit 2
    fi

    if [[ "$1" == suspend-worker ]]; then
      vm suspend "$node"
    else
      vm resume "$node"
    fi
    ;;
  *)
    echo 'Usage: scripts/vm/main.sh manager|doctor|up|status|halt|resume|destroy|suspend-worker|resume-worker [worker]'
    ;;
esac
