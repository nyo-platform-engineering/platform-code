#!/usr/bin/env bash
# Manage Vagrant guests and export a kubeconfig for this lab only.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
root="$PWD"
state="$root/.state"
vm() {
  # The subshell keeps Vagrant's working directory local to this command.
  (cd "$root/local" && vagrant "$@")
}

case "${1:-help}" in
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
      echo 'Choose aat-worker-1 or aat-worker-2.' >&2
      exit 2
    fi

    if [[ "$1" == suspend-worker ]]; then
      vm suspend "$node"
    else
      vm resume "$node"
    fi
    ;;
  *)
    echo 'Usage: vm.sh doctor|up|status|halt|resume|destroy|suspend-worker|resume-worker [worker]'
    ;;
esac
