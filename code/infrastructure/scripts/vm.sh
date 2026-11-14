#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
root="$PWD"
state="$root/.state"
vm() { (cd "$root/local" && vagrant "$@"); }
doctor() {
  command -v vagrant >/dev/null || { echo 'Vagrant is missing; see README.md (Linux VM setup).' >&2; return 1; }
  vagrant plugin list | grep -q '^vagrant-libvirt ' || { echo 'Install vagrant-libvirt; see README.md.' >&2; return 1; }
  if [[ "${AAT_VM_DRIVER:-kvm}" == kvm && ( ! -r /dev/kvm || ! -w /dev/kvm ) ]]; then
    echo 'KVM is unavailable here. Enable /dev/kvm access or set AAT_VM_DRIVER=qemu for slow software emulation.' >&2
    return 1
  fi
}
case "${1:-help}" in
  doctor) doctor ;;
  up)
    doctor
    mkdir -p "$state"
    chmod 700 "$state"
    if [[ ! -f "$state/cluster-token" ]]; then
      (umask 077; python3 -c 'import secrets; print(secrets.token_hex(32))' > "$state/cluster-token")
    fi
    # Control plane must be ready before provisioning the two agents.
    vm up aat-control --provider=libvirt
    vm up aat-worker-1 aat-worker-2 --provider=libvirt --no-parallel
    umask 077
    temporary="$(mktemp "$state/kubeconfig.XXXXXX")"
    trap 'rm -f "$temporary"' EXIT
    vm ssh aat-control -c 'sudo cat /etc/rancher/k3s/k3s.yaml' 2>/dev/null |
      sed "s/127.0.0.1/${AAT_NETWORK_PREFIX:-192.168.56}.10/g" | tr -d '\r' > "$temporary"
    [[ -s "$temporary" ]] || { echo 'VM kubeconfig export failed.' >&2; exit 1; }
    # Keep the kubeconfig private and isolated from the host default context.
    mv "$temporary" "$state/kubeconfig"
    KUBECONFIG="$state/kubeconfig" kubectl wait --for=condition=Ready node/aat-control node/aat-worker-1 node/aat-worker-2 --timeout=300s
    ;;
  status) vm status ;;
  halt) vm halt ;;
  resume) vm resume ;;
  destroy)
    # Vagrant provides its own confirmation prompt; credentials remain for rebuilds.
    vm destroy
    ;;
  suspend-worker|resume-worker)
    node="${2:-aat-worker-1}"
    [[ "$node" == aat-worker-1 || "$node" == aat-worker-2 ]] || { echo 'Choose aat-worker-1 or aat-worker-2.' >&2; exit 2; }
    if [[ "$1" == suspend-worker ]]; then vm suspend "$node"; else vm resume "$node"; fi
    ;;
  *) echo 'Usage: vm.sh doctor|up|status|halt|resume|destroy|suspend-worker|resume-worker [worker]' ;;
esac
