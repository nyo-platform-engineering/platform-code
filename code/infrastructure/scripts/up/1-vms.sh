#!/usr/bin/env bash
# Start the control plane and workers, then export their kubeconfig.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

state="$root/.state"
vagrant_command() {
  # Vagrant reads local/Vagrantfile. The subshell keeps this directory change
  # from affecting later commands, whose paths are relative to infrastructure/.
  (cd "$root/local" && vagrant "$@")
}

preflight_check() {
  # Vagrant starts guests, Python generates the token, and kubectl waits for nodes.
  # sed and tr convert the exported kubeconfig to the VM's reachable address.
  for tool in vagrant python3 kubectl sed tr mktemp; do
    if ! command -v "$tool" >/dev/null; then
      echo "Missing $tool; see README.md." >&2
      return 1
    fi
  done

  # This step always uses Vagrant's libvirt provider.
  if ! vagrant plugin list | grep -q '^vagrant-libvirt '; then
    echo 'Install vagrant-libvirt; see README.md.' >&2
    return 1
  fi

  # Software emulation does not need /dev/kvm access.
  if [[ "${AAT_VM_DRIVER:-kvm}" == kvm && ( ! -r /dev/kvm || ! -w /dev/kvm ) ]]; then
    echo 'KVM is unavailable here. Enable /dev/kvm access or set AAT_VM_DRIVER=qemu for slow software emulation.' >&2
    return 1
  fi
}

preflight_check

# Remaining steps (--check-only exits before running them):
# prepare_vm_credentials - create private state and a stable token for workers joining k3s.
# start_control_plane - start the VM hosting the Kubernetes API before workers join.
# start_workers - start and provision both worker VMs.
# export_kubeconfig - copy cluster access settings to the laptop and use the VM API address.
# wait_for_nodes - wait until all three Kubernetes nodes report Ready.

# Used by task doctor to inspect prerequisites without starting any guests.
if [[ "${1:-}" == --check-only ]]; then
  exit 0
fi

prepare_vm_credentials() {
  # Only the current user should be able to read the cluster's local credentials.
  mkdir -p "$state"
  chmod 700 "$state"
  # k3s workers use this shared token to join the control plane. Keep it between
  # runs so restarting the lab does not give existing nodes a different token.
  if [[ ! -f "$state/cluster-token" ]]; then
    (
      # New files are readable only by their owner. The subshell limits this
      # permission setting to token creation rather than the rest of startup.
      umask 077
      python3 -c 'import secrets; print(secrets.token_hex(32))' > "$state/cluster-token"
    )
  fi
}
prepare_vm_credentials

start_control_plane() {
  # Workers need the control plane to be provisioned first.
  # Vagrant creates a missing guest or starts an existing one. Its provisioner
  # installs the k3s server that workers will connect to.
  vagrant_command up aat-control --provider=libvirt
}
start_control_plane

start_workers() {
  # Start both workers after the server so it can join k3s.
  vagrant_command up aat-worker-1 aat-worker-2 --provider=libvirt --no-parallel
}
start_workers

export_kubeconfig() {
  # Export to a private temporary file so failure preserves the old kubeconfig.
  umask 077
  local temporary
  temporary="$(mktemp "$state/kubeconfig.XXXXXX")"
  # If SSH, conversion, or the final move fails, remove the unfinished export.
  trap 'rm -f "$temporary"' EXIT
  # The guest kubeconfig contains client credentials and its API address.
  # Replace guest-local 127.0.0.1 with the control plane's laptop-reachable IP,
  # and strip carriage returns that may be introduced by the SSH terminal.
  vagrant_command ssh aat-control -c 'sudo cat /etc/rancher/k3s/k3s.yaml' 2>/dev/null |
    sed "s/127.0.0.1/${AAT_NETWORK_PREFIX:-192.168.56}.10/g" |
    tr -d '\r' > "$temporary"

  if [[ ! -s "$temporary" ]]; then
    echo 'VM kubeconfig export failed.' >&2
    exit 1
  fi
  # Keep the kubeconfig private and isolated from the host default context.
  mv "$temporary" "$state/kubeconfig"
  # The export succeeded and the temporary path no longer exists.
  trap - EXIT
}
export_kubeconfig

wait_for_nodes() {
  # A booted VM may still be starting Kubernetes. Stop here until all three
  # nodes report Ready, so the next script can safely prepare cluster resources.
  # Use the lab kubeconfig explicitly to avoid selecting another host context.
  KUBECONFIG="$state/kubeconfig" kubectl wait --for=condition=Ready \
    node/aat-control node/aat-worker-1 node/aat-worker-2 --timeout=300s
}
wait_for_nodes
