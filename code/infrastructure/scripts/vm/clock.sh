#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../local"

case "${1:-status}" in
  sync)
    # Only the clock provisioner runs; k3s and application configuration stay live.
    vagrant provision --provision-with clock
    ;;
  status)
    if (( $# <= 1 )); then
      exec python3 ../scripts/vm/clock_status.py
    fi
    if [[ $# != 2 || "$2" != --verbose ]]; then
      echo 'Usage: clock.sh status [--verbose]' >&2
      exit 2
    fi
    for node in aat-control aat-worker-1 aat-worker-2; do
      printf '\n%s\n' "$node"
      vagrant ssh "$node" --no-tty -c 'chronyc tracking && chronyc -n sources' </dev/null
    done
    ;;
  *) echo 'Usage: clock.sh sync|status' >&2; exit 2 ;;
esac
