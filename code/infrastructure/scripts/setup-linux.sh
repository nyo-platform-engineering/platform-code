#!/usr/bin/env bash
# Install the Linux CLI dependencies without relying on distro-specific packages.
set -euo pipefail

install_dir="${INSTALL_DIR:-/usr/local/bin}"
kubectl_version="${KUBECTL_VERSION:-v1.34.5}"

for tool in curl openssl tar sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "Missing prerequisite: $tool. On Ubuntu, run: sudo apt-get install curl openssl tar coreutils" >&2
    exit 1
  }
done

case "$(uname -m)" in
  x86_64) kubectl_arch=amd64 ;;
  aarch64|arm64) kubectl_arch=arm64 ;;
  *) echo "Unsupported Linux architecture for kubectl: $(uname -m)" >&2; exit 1 ;;
esac

temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT

install_file() {
  local source=$1 destination=$2
  if [[ $EUID -eq 0 || -w "$install_dir" ]]; then
    install -m 0755 "$source" "$destination"
  elif command -v sudo >/dev/null 2>&1; then
    sudo install -o root -g root -m 0755 "$source" "$destination"
  else
    echo "Cannot write to $install_dir and sudo is unavailable. Set INSTALL_DIR to a writable directory on PATH." >&2
    exit 1
  fi
}

if command -v kubectl >/dev/null 2>&1; then
  echo "kubectl already installed: $(command -v kubectl)"
else
  kubectl_url="https://dl.k8s.io/release/${kubectl_version}/bin/linux/${kubectl_arch}/kubectl"
  echo "Installing kubectl ${kubectl_version}..."
  curl --proto '=https' --tlsv1.2 -fsSL "$kubectl_url" -o "$temp_dir/kubectl"
  curl --proto '=https' --tlsv1.2 -fsSL "$kubectl_url.sha256" -o "$temp_dir/kubectl.sha256"
  printf '%s  %s\n' "$(<"$temp_dir/kubectl.sha256")" "$temp_dir/kubectl" | sha256sum --check --status
  install_file "$temp_dir/kubectl" "$install_dir/kubectl"
fi

if command -v helm >/dev/null 2>&1; then
  echo "Helm already installed: $(command -v helm)"
else
  echo 'Installing Helm...'
  curl --proto '=https' --tlsv1.2 -fsSL \
    https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 \
    -o "$temp_dir/get-helm-3"
  chmod 0700 "$temp_dir/get-helm-3"
  HELM_INSTALL_DIR="$install_dir" "$temp_dir/get-helm-3"
fi

if command -v k3d >/dev/null 2>&1; then
  echo "k3d already installed: $(command -v k3d)"
else
  echo 'Installing k3d...'
  curl --proto '=https' --tlsv1.2 -fsSL \
    https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh \
    -o "$temp_dir/get-k3d"
  chmod 0700 "$temp_dir/get-k3d"
  K3D_INSTALL_DIR="$install_dir" "$temp_dir/get-k3d"
fi

printf '\nInstalled CLI versions:\n'
kubectl version --client
helm version --short
k3d version
