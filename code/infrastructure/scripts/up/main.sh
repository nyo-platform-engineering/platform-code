#!/usr/bin/env bash
# Entry point for task up: run the VM startup steps in order.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

novm=0
local_build=0
for arg in "$@"; do
  case "$arg" in
    --novm) novm=1 ;;
    --local-build) local_build=1 ;;
    *)
      echo "Unknown option $arg. Usage: task up            (three VMs)" >&2
      echo "                              task up -- --novm (k3d containers, development only)" >&2
      echo "                              task up -- --novm --local-build (native local images)" >&2
      exit 2
      ;;
  esac
done

if (( local_build && !novm )); then
  echo '--local-build requires --novm.' >&2
  exit 2
fi

# Startup always targets this lab, even if the caller selected another cluster.
export KUBECONFIG="$root/.state/kubeconfig"

# Each numbered script checks its own prerequisites before changing anything.
if (( novm )); then
  # k3d: the same three node names as Docker containers. Shared kernel, so not for the demo.
  export AAT_NOVM=1
  bash scripts/up/1-k3d.sh
else
  if [[ "$lab_mode" == k3d ]]; then
    echo 'This checkout runs the k3d lab. Use task up -- --novm, or task destroy before starting VMs.' >&2
    exit 2
  fi
  bash scripts/up/1-vms.sh
fi
bash scripts/up/2-cluster.sh
if (( local_build )); then
  python3 scripts/up/local-build.py build
fi
bash scripts/up/3-argocd.sh
bash scripts/up/4-apps.sh
