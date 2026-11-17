#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

# Print the requested admin password from local state or Kubernetes.
case "${1:-grafana}" in
  grafana)
    cat .state/grafana-password
    ;;
  argocd)
    kube get secret argocd-initial-admin-secret -n argocd -o go-template='{{.data.password | base64decode}}{{"\n"}}'
    ;;
  *)
    echo 'Choose grafana or argocd.' >&2
    exit 2
    ;;
esac
