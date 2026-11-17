#!/usr/bin/env bash
# Refresh the registered VM context, then open K9s across all namespaces.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

if ! command -v k9s >/dev/null; then
  echo 'k9s is missing. Install it before running task k9s.' >&2
  exit 1
fi

bash scripts/context/main.sh
exec k9s --kubeconfig "${KUBE_CONTEXT_FILE:-$HOME/.kube/config}" \
  --context "${KUBE_CONTEXT_NAME:-aat-local}" --all-namespaces
