#!/usr/bin/env bash
# Build the exact archived upstream releases when their registry images disappear.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
cluster="${CLUSTER_NAME:-dev}"
arch="$(kubectl --context "k3d-$cluster" get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')"
[[ "$arch" == arm64 || "$arch" == amd64 ]] || { echo "Unsupported node architecture: $arch" >&2; exit 1; }
work="$(mktemp -d "${TMPDIR:-/tmp}/gitlab-minio.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/context"
cp images/minio/Dockerfile "$work/context/Dockerfile"
for component in minio mc; do
  if [[ "$component" == minio ]]; then
    version=RELEASE.2025-09-07T16-13-09Z
    revision=07c3a429bfed433e49018cb0f78a52145d4bedeb
  else
    version=RELEASE.2025-08-13T08-35-41Z
    revision=7394ce0dd2a80935aded936b09fa12cbb3cb8096
  fi
  image="local/$component:$version"
  existing="$(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}/{{.Architecture}}' 2>/dev/null || true)"
  if [[ "$existing" == "$revision/$arch" ]]; then
    echo "Reusing verified $image ($arch)."
    continue
  fi
  git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$version" "https://github.com/minio/$component.git" "$work/$component"
  [[ "$(git -C "$work/$component" rev-parse HEAD)" == "$revision" ]] || { echo "Unexpected $component source revision" >&2; exit 1; }
  (
    cd "$work/$component"
    # Compile on the host to avoid competing with GitLab for the runtime VM's RAM.
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOMAXPROCS=2 go build -p 2 -trimpath -o "$work/context/$component" .
  )
  docker build --platform "linux/$arch" --target "$component" \
    --label "org.opencontainers.image.source=https://github.com/minio/$component" \
    --label "org.opencontainers.image.revision=$revision" \
    -t "local/$component:$version" "$work/context"
done
k3d image import -c "$cluster" \
  local/minio:RELEASE.2025-09-07T16-13-09Z \
  local/mc:RELEASE.2025-08-13T08-35-41Z
echo 'Pinned MinIO images built and imported; Argo CD owns their workloads.'
