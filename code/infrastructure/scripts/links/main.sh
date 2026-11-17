#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

# Print the local hostname URLs and direct VM access addresses.
links
