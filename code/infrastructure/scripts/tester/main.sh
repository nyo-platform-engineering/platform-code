#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

# Keep the three official Tester Agent operations together.
case "${1:-}" in
  install)
    kube apply -f https://raw.githubusercontent.com/susTuna/if4031-tester-agent/ebb55261711123a9a211739ffbf140a275caa6da/manifest.yaml
    ;;
  list)
    kube exec -n tester-agent daemonset/tester-agent -- /app list
    ;;
  practice)
    if [[ -z "${2:-}" ]]; then
      echo 'Pass the scenario name.' >&2
      exit 2
    fi
    kube exec -n tester-agent daemonset/tester-agent -- /app run practice "$2"
    ;;
  *)
    echo 'Usage: tester.sh install|list|practice [scenario]' >&2
    exit 2
    ;;
esac
