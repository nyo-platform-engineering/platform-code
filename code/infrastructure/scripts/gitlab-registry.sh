#!/usr/bin/env bash
# Containerd reads hosts.toml dynamically; no node restart or data reset is needed.
set -euo pipefail
export MSYS_NO_PATHCONV=1
nodes="$(kubectl --context "k3d-${CLUSTER_NAME:-dev}" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
while IFS= read -r node; do
  node="${node%$'\r'}"
  [[ -n "$node" ]] || continue
  docker exec -i "$node" sh -s <<'EOF'
set -eu
for name in gitlab.localhost registry.localhost; do
  if ! grep -q "^127.0.0.1 $name$" /etc/hosts; then
    printf '127.0.0.1 %s\n' "$name" >> /etc/hosts
  fi
done
directory=/var/lib/rancher/k3s/agent/etc/containerd/certs.d/registry.localhost
mkdir -p "$directory"
cat > "$directory/hosts.toml" <<'CONFIG'
server = "http://registry.localhost"
[host."http://registry.localhost"]
  capabilities = ["pull", "resolve"]
CONFIG
EOF
done <<< "$nodes"
echo 'Local GitLab HTTP registry configured on k3d nodes.'
