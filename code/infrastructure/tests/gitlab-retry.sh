#!/usr/bin/env bash
# No cluster changes: exercise storage recovery with mocked Kubernetes commands.
set -euo pipefail
scripts="$(cd "$(dirname "${BASH_SOURCE[0]}")/../scripts" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
sed -n '/^storage_ready() {/,/^case /p' "$scripts/gitlab.sh" | sed '$d' > "$work/functions.sh"
cat > "$work/check.sh" <<'BASH'
set -euo pipefail
source "$1/functions.sh"
kube() {
  case "$*" in
    'get statefulset '*) return 0 ;;
    'rollout status '*) return 0 ;;
    'get jobs '*)
      [[ "$MODE" != unavailable ]] || return 1
      printf '%s\n' gitlab-create-buckets-old gitlab-create-buckets-current unrelated-job
      ;;
    'get job gitlab-create-buckets-old '*)
      [[ "$MODE" != failed ]] || printf True
      ;;
    'get job gitlab-create-buckets-current '*) return 0 ;;
    'delete job '*) printf 'DELETE %s\n' "$3" ;;
    *) echo "Unexpected command: $*" >&2; return 1 ;;
  esac
}
ready() { printf 'READY %s\n' "$1"; }
storage_ready
BASH

for mode in failed healthy unavailable; do
  actual=0
  # Run in a separate shell so errexit remains enabled inside storage_ready.
  set +e
  output="$(MODE="$mode" bash "$work/check.sh" "$work" 2>&1)"
  actual=$?
  set -e
  case "$mode" in
    failed)
      [[ "$actual" == 0 && "$output" == $'DELETE gitlab-create-buckets-old\nREADY gitlab-services' ]]
      ;;
    healthy)
      [[ "$actual" == 0 && "$output" == 'READY gitlab-services' ]]
      ;;
    unavailable)
      [[ "$actual" != 0 && "$output" != *DELETE* && "$output" != *READY* ]]
      ;;
  esac
  printf 'Passed storage retry: %s\n' "$mode"
done
