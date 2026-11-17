#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

# Stop local gateway access and halt the VMs, retaining their disks.
bash scripts/vm.sh halt
