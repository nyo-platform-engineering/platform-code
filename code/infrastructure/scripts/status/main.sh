#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../up/common.sh"

# Inspect the selected cluster and the laptop gateway process.
kube get nodes -o wide
kube get applications -n argocd
kube get pods,services,statefulsets,hpa,pdb -n "$namespace" -o wide
kube get pods -n monitoring -o wide
kube get pods,services -n traefik -o wide
kube get pods,services -n radar -o wide
python3 scripts/up/gateway.py status
