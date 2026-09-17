#!/usr/bin/env bash
# No cluster or container runtime is changed; command mocks exercise preflight.
set -euo pipefail
bootstrap="$(cd "$(dirname "${BASH_SOURCE[0]}")/../scripts" && pwd)/bootstrap.sh"

check() {
  local mode="$1" expected="$2" actual=0 output
  output="$(
    command() {
      if [[ "$mode" == missing && "$1" == -v && "$2" == docker ]]; then
        return 1
      fi
      builtin command "$@"
    }
    docker() {
      [[ "$mode" != unreachable ]] || return 1
      if [[ "$mode" == windows ]]; then
        printf 'windows\n'
      elif [[ "$*" == *Architecture* ]]; then
        printf 'linux/arm64\n'
      else
        printf 'linux\n'
      fi
    }
    kubectl() { :; }
    helm() { :; }
    k3d() { :; }
    set -- doctor
    source "$bootstrap" 2>&1
  )" || actual=$?
  [[ "$actual" == "$expected" && "$output" == *"$3"* ]] || {
    printf 'Failed case %s: exit=%s, output=%s\n' "$mode" "$actual" "$output" >&2
    return 1
  }
  printf 'Passed preflight: %s\n' "$mode"
}

check linux 0 'Docker engine: linux/arm64'
check unreachable 1 'Cannot reach Docker'
check windows 1 'A Linux Docker engine is required'
check missing 1 'Missing docker on PATH'
