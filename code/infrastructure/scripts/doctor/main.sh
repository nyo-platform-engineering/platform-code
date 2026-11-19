#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

# Check startup prerequisites without starting or modifying anything.
# task doctor -- --novm checks the k3d (development) mode instead of the VMs.
if [[ "${1:-}" == --novm || "$lab_mode" == k3d ]]; then
  bash scripts/up/1-k3d.sh --check-only
else
  bash scripts/up/1-vms.sh --check-only
fi
