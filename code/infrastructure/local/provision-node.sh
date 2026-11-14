#!/usr/bin/env bash
set -euo pipefail
role="$1"; node_name="$2"; node_ip="$3"; server_ip="$4"; version="$5"
[[ "$role" == server || "$role" == agent ]] || exit 2
apt-get update -qq
apt-get install -y -qq curl ca-certificates
install -d -m 700 /etc/rancher/k3s
umask 077
token="$(cat /tmp/aat-k3s-token)"
[[ "$token" =~ ^[a-f0-9]{64}$ ]] || { echo 'Invalid cluster token' >&2; exit 1; }
iface="$(ip -o -4 addr show | awk -v ip="$node_ip" '$4 ~ ("^" ip "/") {print $2; exit}')"
[[ -n "$iface" ]] || { echo 'Private-network interface not found' >&2; exit 1; }
{
  printf 'node-name: %s\nnode-ip: %s\nflannel-iface: %s\ntoken: %s\n' "$node_name" "$node_ip" "$iface" "$token"
  if [[ "$role" == server ]]; then
    printf 'tls-san:\n  - %s\ndisable:\n  - traefik\n  - servicelb\nwrite-kubeconfig-mode: "0600"\n' "$server_ip"
  else
    printf 'server: https://%s:6443\n' "$server_ip"
  fi
} > /etc/rancher/k3s/config.yaml
# Keep the embedded network-policy controller enabled for storage isolation.
curl --fail --show-error --silent --location --retry 5 https://get.k3s.io -o /tmp/install-k3s.sh
INSTALL_K3S_VERSION="$version" INSTALL_K3S_EXEC="$role" sh /tmp/install-k3s.sh
rm -f /tmp/aat-k3s-token /tmp/install-k3s.sh
