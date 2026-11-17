#!/usr/bin/env bash
# Entry point for task up: run the VM startup steps in order.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

if (( $# > 0 )); then
  echo 'task up uses the VM lab and does not accept options.' >&2
  exit 2
fi

# Startup always targets this lab, even if the caller selected another cluster.
export KUBECONFIG="$root/.state/kubeconfig"

# Each numbered script checks its own prerequisites before changing anything.
bash scripts/up/1-vms.sh
bash scripts/up/2-cluster.sh
bash scripts/up/3-argocd.sh
bash scripts/up/4-apps.sh
