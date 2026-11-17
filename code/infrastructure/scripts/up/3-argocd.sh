#!/usr/bin/env bash
# Install Argo CD once; subsequent configuration is managed through Git.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

preflight_check() {
  # Tool checks: kubectl waits for Argo CD, Helm installs it, and awk/tr read the pinned chart version.
  for tool in kubectl helm awk tr; do
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

  # The API must be ready before checking or installing Argo CD.
  if ! kube get --raw=/readyz --request-timeout=15s >/dev/null; then
    echo 'Kubernetes API is not ready. Start the VM cluster before bootstrapping Argo CD.' >&2
    return 1
  fi
}

preflight_check

# Remaining steps:
# install_argocd_if_missing - install the pinned Helm chart only when Argo CD is absent.
# wait_for_argocd - wait for the server, repository server, and application controller rollouts.

install_argocd_if_missing() {
  local existing_argocd
  local argocd_chart_version

  # An absent Deployment produces empty output. An API or permission error
  # still fails the script; it must not be mistaken for a missing installation.
  existing_argocd="$(kube get deployment argocd-server -n argocd --ignore-not-found -o name)"

  if [[ -z "$existing_argocd" ]]; then
    # Helm seeds the controller on the first run. Once it exists, Argo CD
    # manages its own configuration from Git, so bootstrap skips Helm upgrades.
    # The chart's pinned targetRevision is on the line after "chart: argo-cd".
    argocd_chart_version="$(
      awk '/chart: argo-cd/ { getline; print $2; exit }' \
        argo-apps/platform/01-cd/argocd/app.yaml |
        tr -d '\r\047\042'
    )"

    # Avoid installing an unintended chart version if the manifest changed.
    if [[ -z "$argocd_chart_version" ]]; then
      echo 'Cannot read the pinned Argo CD chart version.' >&2
      exit 1
    fi

    # Use the same chart version and values as the GitOps Application.
    # --wait lets Helm wait for resources instead of just submitting manifests.
    helmc upgrade --install argocd argo-cd --repo https://argoproj.github.io/argo-helm \
      --version "$argocd_chart_version" -n argocd \
      -f argo-apps/platform/01-cd/argocd/values.yaml --wait --timeout 10m
  fi
}
install_argocd_if_missing

wait_for_argocd() {
  # Wait for the API, Git fetcher, and reconciler before registering applications.
  # This also runs for an existing installation: its pods may still be starting
  # after the VMs resume. Rollout checks finish immediately when already ready.
  kube rollout status deployment/argocd-server -n argocd --timeout=300s
  kube rollout status deployment/argocd-repo-server -n argocd --timeout=300s
  kube rollout status statefulset/argocd-application-controller -n argocd --timeout=300s
}
wait_for_argocd
