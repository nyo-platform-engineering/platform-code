#!/usr/bin/env bash
# Requires the ownership policy in the cluster. All workload writes are dry runs.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export MSYS_NO_PATHCONV=1
directory="$(mktemp -d './.ownership-test.XXXXXX')"
trap 'rm -f "$directory/compliant.yaml" "$directory/invalid.yaml" "$directory/result" "$directory/workload.yaml"; rmdir "$directory"' EXIT
context="k3d-${CLUSTER_NAME:-dev}"
policy="${OWNERSHIP_POLICY_NAME:-require-workload-owner}"
mode="$(kubectl --context "$context" get validatingpolicy "$policy" -o jsonpath='{.spec.validationActions[0]}')"
[[ "$mode" == Audit || "$mode" == Deny ]] || { echo "Unsupported ownership mode: $mode" >&2; exit 1; }
check_invalid() {
  local status=0
  "$@" > "$directory/result" 2>&1 || status=$?
  if [[ "$mode" == Audit ]]; then
    [[ "$status" == 0 ]] || { cat "$directory/result" >&2; return 1; }
  else
    [[ "$status" != 0 ]] || { echo 'FAIL: invalid ownership was admitted in Deny mode' >&2; return 1; }
    grep -q 'Pods and workload templates must have platform.local/owner' "$directory/result" || {
      cat "$directory/result" >&2
      return 1
    }
  fi
}
for namespace in dev kube-system kyverno; do
  helm template owner-label-test charts/deployment -n "$namespace" \
    -f argo-apps/dev/go-demo/values.yaml > "$directory/compliant.yaml"
  kubectl --context "$context" apply -n "$namespace" --dry-run=server \
    -f "$directory/compliant.yaml" >/dev/null
  for owner in '' Platform_Team null; do
    option=--set-string
    [[ "$owner" != null ]] || option=--set
    helm template owner-label-test charts/deployment -n "$namespace" \
      -f argo-apps/dev/go-demo/values.yaml \
      "$option" "labels.platform\\.local/owner=$owner" > "$directory/invalid.yaml"
    check_invalid kubectl --context "$context" apply -n "$namespace" --dry-run=server \
      -f "$directory/invalid.yaml"
  done
  # Check direct Pods and the deeper CronJob template path independently.
  for kind in pod cronjob; do
    if [[ "$kind" == pod ]]; then
      kubectl run owner-label-test --image=alpine:3.23 --restart=Never \
        --dry-run=client -o yaml > "$directory/workload.yaml"
      patch='{"metadata":{"labels":{"platform.local/owner":"platform-team"}}}'
    else
      kubectl create cronjob owner-label-test --image=alpine:3.23 \
        --schedule='*/5 * * * *' --dry-run=client -o yaml > "$directory/workload.yaml"
      patch='{"spec":{"jobTemplate":{"spec":{"template":{"metadata":{"labels":{"platform.local/owner":"platform-team"}}}}}}}'
    fi
    check_invalid kubectl --context "$context" create -n "$namespace" --dry-run=server \
      -f "$directory/workload.yaml"
    kubectl patch --local -f "$directory/workload.yaml" --type=merge -p "$patch" \
      -o yaml > "$directory/compliant.yaml"
    kubectl --context "$context" create -n "$namespace" --dry-run=server \
      -f "$directory/compliant.yaml" >/dev/null
  done
  echo "PASS: compliant workloads admitted; invalid ownership follows $mode mode in $namespace"
done
