#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

# Check VM startup prerequisites without starting or modifying guests.
bash scripts/up/1-vms.sh --check-only
