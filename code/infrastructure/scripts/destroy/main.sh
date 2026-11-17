#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

# Let Vagrant confirm removal of the VMs and their disks.
bash scripts/vm.sh destroy
