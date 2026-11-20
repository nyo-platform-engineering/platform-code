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
      rm -f "$state/local-build/images.json"
      ;;
    # Freezing a container is the k3d equivalent of suspending a VM.
    suspend-worker) docker pause "$(container "${2:-aat-worker-1}")" ;;
    resume-worker) docker unpause "$(container "${2:-aat-worker-1}")" ;;
    stop-worker) docker stop --time 0 "$(container "${2:-aat-worker-1}")" ;;
    start-worker) docker start "$(container "${2:-aat-worker-1}")" ;;
    manager) echo 'k3d mode has no VMs; use docker ps or task k9s.' >&2; exit 2 ;;
    *) echo 'Usage (k3d): scripts/vm/main.sh doctor|up|status|halt|resume|destroy|suspend-worker|resume-worker|stop-worker|start-worker [worker]' ;;
  esac
  exit
fi
vm() {
  # The subshell keeps Vagrant's working directory local to this command.
  (cd "$root/local" && vagrant "$@")
}

worker_power() {
  local operation="$1" node="$2" identity_file domain_uuid domain_name domain_state
  identity_file="$root/local/.vagrant/machines/$node/libvirt/id"
  [[ -s "$identity_file" ]] || { echo "Missing local libvirt identity: $node" >&2; return 1; }
  domain_uuid="$(cat "$identity_file")"
  [[ "$domain_uuid" =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]] || return 1
  domain_name="$(virsh -c qemu:///system domname "$domain_uuid")"
  [[ "$domain_name" == "aat-m2$node" ]] || { echo "Unexpected domain for $node: $domain_name" >&2; return 1; }
  domain_state="$(LC_ALL=C virsh -c qemu:///system domstate "$domain_uuid")"
  if [[ "$operation" == stop-worker ]]; then
    if [[ "$domain_state" != 'shut off' && "$domain_state" != crashed ]]; then
      # Cut power directly: vagrant-libvirt's graceful shutdown can time out even
      # after Kubernetes observes NotReady. destroy does not undefine/delete disks.
      virsh -c qemu:///system destroy "$domain_uuid"
    fi
    [[ "$(LC_ALL=C virsh -c qemu:///system domstate "$domain_uuid")" == 'shut off' ]]
  else
    case "$domain_state" in
      'shut off'|crashed) virsh -c qemu:///system start "$domain_uuid" ;;
      paused) virsh -c qemu:///system resume "$domain_uuid" ;;
      running) echo "$node is already running" ;;
      *) echo "Cannot start $node in state: $domain_state" >&2; return 1 ;;
    esac
  fi
}

worker_freeze() {
  local operation="$1" node="$2" identity_file domain_uuid domain_name domain_state
  identity_file="$root/local/.vagrant/machines/$node/libvirt/id"
  [[ -s "$identity_file" ]] || { echo "Missing local libvirt identity: $node" >&2; return 1; }
  domain_uuid="$(cat "$identity_file")"
  [[ "$domain_uuid" =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]] || return 1
  domain_name="$(virsh -c qemu:///system domname "$domain_uuid")"
  [[ "$domain_name" == "aat-m2$node" ]] || { echo "Unexpected domain for $node: $domain_name" >&2; return 1; }
  domain_state="$(LC_ALL=C virsh -c qemu:///system domstate "$domain_uuid")"
  if [[ "$operation" == suspend-worker ]]; then
    case "$domain_state" in
      running) virsh -c qemu:///system suspend "$domain_uuid" ;;
      paused) echo "$node is already suspended" ;;
      *) echo "Cannot suspend $node in state: $domain_state" >&2; return 1 ;;
    esac
    [[ "$(LC_ALL=C virsh -c qemu:///system domstate "$domain_uuid")" == paused ]]
  else
    # Resume the existing guest only: Vagrant resume also reruns 'always'
    # provisioners, which can hang on clock sync and leave an action locked.
    case "$domain_state" in
      paused) virsh -c qemu:///system resume "$domain_uuid" ;;
      running) echo "$node is already running" ;;
      *) echo "Cannot resume $node in state: $domain_state" >&2; return 1 ;;
    esac
    [[ "$(LC_ALL=C virsh -c qemu:///system domstate "$domain_uuid")" == running ]]
  fi
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
  suspend-worker|resume-worker|stop-worker|start-worker)
    node="${2:-aat-worker-1}"
    if [[ "$node" != aat-worker-1 && "$node" != aat-worker-2 ]]; then
      echo 'Choose aat-worker-1 or aat-worker-2; the control plane must stay running during failure tests.' >&2
      exit 2
    fi

    case "$1" in
      suspend-worker|resume-worker) worker_freeze "$1" "$node" ;;
      stop-worker|start-worker) worker_power "$1" "$node" ;;
    esac
    ;;
  *)
    echo 'Usage: scripts/vm/main.sh manager|doctor|up|status|halt|resume|destroy|suspend-worker|resume-worker|stop-worker|start-worker [worker]'
    ;;
esac
