#!/usr/bin/env bash
# Import the VM cluster under its own name in the user's kubeconfig.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

for tool in kubectl python3; do
  if ! command -v "$tool" >/dev/null; then
    echo "Missing $tool; see README.md." >&2
    exit 1
  fi
done

python3 scripts/context/register.py \
  --source "$PWD/.state/kubeconfig" \
  --target "${KUBE_CONTEXT_FILE:-$HOME/.kube/config}" \
  --name "${KUBE_CONTEXT_NAME:-aat-local}"
