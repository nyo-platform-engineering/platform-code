#!/usr/bin/env bash
# Register applications, then start background loopback access through Traefik.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

port="${GATEWAY_PORT:-80}"

preflight_check() {
  # Reject malformed ports before running any cluster commands.
  if [[ ! "$port" =~ ^[1-9][0-9]{0,4}$ ]] || (( port > 65535 )); then
    echo 'Gateway PORT must be 1..65535.' >&2
    return 2
  fi

  if [[ ! -f "$KUBECONFIG" ]]; then
    echo 'Kubeconfig missing. Run task up first.' >&2
    return 1
  fi

  # Tool checks: kubectl checks readiness; Python runs the background TCP listener.
  for tool in kubectl python3; do
    if ! command -v "$tool" >/dev/null; then
      echo "Missing $tool; see README.md." >&2
      return 1
    fi
  done

  # Binding a low port as a regular user requires sudo.
  if (( port < 1024 && EUID != 0 )); then
    if ! command -v sudo >/dev/null; then
      echo 'sudo is needed for ports below 1024; use PORT=8080 instead.' >&2
      return 1
    fi
  fi

  if [[ ! -f argo-apps/root.yaml ]]; then
    echo 'Root Application manifest missing: argo-apps/root.yaml.' >&2
    return 1
  fi

  # Argo CD must have installed its Application CRD before registering the root.
  kube wait --for=condition=Established crd/applications.argoproj.io --timeout=60s

}

preflight_check

# Remaining steps:
# register_root_application - register the GitOps root so Argo CD can deploy its child apps.
# wait_for_gateway - wait for Traefik and the Gateway to exist and become ready.
# start_gateway_listener - start or reuse the background loopback proxy to the VM.
# show_gateway_links - print the URLs and explicit stop command.

register_root_application() {
  # This creates/updates an Argo CD Application, rather than deploying each
  # workload directly. The root points to the Git directory of child apps.
  # Argo CD then fetches the configured revision and reconciles those apps.
  kube apply -f argo-apps/root.yaml
  # Applying the root only registers desired state. Workloads may still be
  # downloading images or starting when this script finishes; inspect status.
  echo 'Root Application registered. Argo CD reconciles platform and AAT from Git asynchronously; use task status.'
}
register_root_application

wait_for_gateway() {
  # Argo CD registers these asynchronously after the root Application is applied.
  # Wait for their creation before asking Kubernetes about readiness.
  local resource
  local deadline
  for resource in deployment/traefik gateway/local; do
    deadline=$((SECONDS + 300))
    until [[ -n "$(kube get "$resource" -n traefik --ignore-not-found -o name 2>/dev/null)" ]]; do
      if (( SECONDS >= deadline )); then
        echo "Timed out waiting for $resource. Check Argo CD sync status." >&2
        return 1
      fi
      sleep 2
    done
  done
  kube rollout status deployment/traefik -n traefik --timeout=300s
  kube wait --for=condition=Programmed gateway/local -n traefik --timeout=180s
}
wait_for_gateway

start_gateway_listener() {
  local python_bin
  local node_address="${AAT_NODE_ADDRESS:-${AAT_NETWORK_PREFIX:-192.168.56}.10}"
  python_bin="$(command -v python3)"

  # Root is needed only to bind a low port. The helper drops back to the sudo
  # caller before detaching; stopping the listener therefore needs no sudo.
  if (( port < 1024 && EUID != 0 )); then
    sudo "$python_bin" "$root/scripts/up/gateway.py" start --port "$port" --host "$node_address"
  else
    "$python_bin" "$root/scripts/up/gateway.py" start --port "$port" --host "$node_address"
  fi
}
start_gateway_listener

show_gateway_links() {
  GATEWAY_PORT="$port" links
  echo 'Gateway runs in the background. Stop with task vm:halt.'
  echo 'Logs: .state/gateway.log'
}
show_gateway_links
