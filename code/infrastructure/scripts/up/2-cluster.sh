#!/usr/bin/env bash
# Prepare namespaces and runtime credentials on the selected cluster.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

preflight_check() {
  # Tool checks: kubectl inspects the cluster; Python validates topology and prepares credentials.
  for tool in kubectl python3; do
    if ! command -v "$tool" >/dev/null; then
      echo "Missing $tool; see README.md." >&2
      return 1
    fi
  done

  # Every Kubernetes command must use an available kubeconfig.
  if [[ ! -f "$KUBECONFIG" ]]; then
    echo 'Kubeconfig missing. Run task up or set KUBECONFIG to your local VM cluster.' >&2
    return 1
  fi

  # All three VM nodes must have joined Kubernetes before cluster preparation.
  if ! kube get node aat-control aat-worker-1 aat-worker-2 >/dev/null; then
    echo 'VM nodes are missing or unreachable. Run task up before preparing the cluster.' >&2
    return 1
  fi

  # Wait for node readiness, then reject an unsuitable topology before creating resources.
  kube wait --for=condition=Ready node/aat-control node/aat-worker-1 node/aat-worker-2 --timeout=180s
  kube get nodes -o json | python3 scripts/up/topology.py
}

preflight_check

# Remaining steps:
# prepare_state_directory - create a private directory for credentials and runtime state.
# create_namespaces - create or update the AAT, monitoring, and Argo CD namespaces.
# prepare_runtime_credentials - reconcile Secrets and mock configuration while retaining credentials.

prepare_state_directory() {
  # task prepare/deploy can run without the VM startup step. Ensure the private
  # directory exists before runtime.py saves passwords and other local state.
  mkdir -p .state
  chmod 700 .state
}
prepare_state_directory

create_namespaces() {
  # Generate namespace manifests so this also works when they already exist.
  # The app, observability stack, and Argo CD each need their own namespace.
  # monitoring must exist before runtime.py creates the Grafana admin Secret.
  # Grafana's pods consume that Secret in the same namespace when Argo CD deploys them.
  # --dry-run prints YAML without creating anything; apply reads that YAML
  # from stdin (-) and creates or updates the namespace in Kubernetes.
  for namespace_name in "$namespace" monitoring argocd; do
    kube create namespace "$namespace_name" --dry-run=client -o yaml |
      kube apply -f -
  done
}
create_namespaces

prepare_runtime_credentials() {
  # Reuse existing credentials rather than rotating them on every startup.
  # runtime.py reconciles the AAT runtime Secret, Grafana admin Secret, and
  # mock start-time configuration before Argo CD starts their consuming pods.
  # Secret values stay in private local files/Kubernetes, outside Git values.
  python3 scripts/up/runtime.py
}
prepare_runtime_credentials
