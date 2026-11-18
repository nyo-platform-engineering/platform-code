#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

get_gateway_port() {
  if [[ -n "${GATEWAY_PORT:-}" ]]; then
    printf '%s\n' "$GATEWAY_PORT"
    return
  fi

  python3 - <<'PY'
import json
from pathlib import Path
try:
    print(json.loads(Path('.state/gateway.json').read_text())['port'])
except (OSError, ValueError, KeyError):
    print(80)
PY
}

print_password() {
  case "$1" in
    grafana)
      cat .state/grafana-password
      ;;
    analyst)
      kube get secret aat-runtime -n "$namespace" \
        -o go-template='{{.data.ANALYST_CLIENT_SECRET | base64decode}}{{"\n"}}'
      ;;
    argocd)
      kube get secret argocd-initial-admin-secret -n argocd \
        -o go-template='{{.data.password | base64decode}}{{"\n"}}'
      ;;
  esac
}

print_credentials() {
  local app="$1" name="$2" suffix="$3"
  printf '%s: http://%s.localhost%s\nUsername: admin\nPassword: ' "$name" "$app" "$suffix"
  print_password "$app"
}

print_all_credentials() {
  local port suffix=""
  port="$(get_gateway_port)"
  if [[ "$port" != 80 ]]; then
    suffix=":$port"
  fi

  print_credentials grafana 'Grafana' "$suffix"
  printf '\n'
  print_credentials argocd 'Argo CD' "$suffix"
}

main() {
  # Individual app selection retains password-only output for scripts.
  case "${1:-all}" in
    all) print_all_credentials ;;
    grafana|argocd|analyst) print_password "$1" ;;
    *)
      echo 'Choose all, grafana, argocd, or analyst.' >&2
      return 2
      ;;
  esac
}

main "$@"
