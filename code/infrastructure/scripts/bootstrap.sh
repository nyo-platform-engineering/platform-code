#!/usr/bin/env bash
# Coordinate startup; each numbered step lives in scripts/up/.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/up/common.sh"

deploy() {
  bash scripts/up/2-cluster.sh
  bash scripts/up/3-argocd.sh
  bash scripts/up/4-apps.sh
}

case "${1:-help}" in
  doctor)
    bash scripts/up/1-vms.sh --check-only
    ;;
  up)
    if (( $# > 1 )); then
      echo 'task up uses the VM lab and does not accept options.' >&2
      exit 2
    fi

    export KUBECONFIG="$root/.state/kubeconfig"
    bash scripts/up/1-vms.sh
    deploy
    ;;
  deploy)
    deploy
    ;;
  prepare)
    bash scripts/up/2-cluster.sh
    ;;
  images)
    if ! command -v docker >/dev/null; then
      echo 'docker is needed for task images.' >&2
      exit 1
    fi

    python3 scripts/images.py | while IFS= read -r image; do
      docker pull "$image"
    done
    ;;
  status)
    kube get nodes -o wide
    kube get applications -n argocd
    kube get pods,services,statefulsets,hpa,pdb -n "$namespace" -o wide
    kube get pods -n monitoring -o wide
    kube get pods,services -n traefik -o wide
    kube get pods,services -n radar -o wide
    python3 scripts/up/gateway.py status
    ;;
  links)
    links
    ;;
  password)
    case "${2:-grafana}" in
      grafana)
        cat .state/grafana-password
        ;;
      argocd)
        kube get secret argocd-initial-admin-secret -n argocd -o go-template='{{.data.password | base64decode}}{{"\n"}}'
        ;;
      *)
        echo 'Choose grafana or argocd.' >&2
        exit 2
        ;;
    esac
    ;;
  argocd-ui)
    kube port-forward -n argocd svc/argocd-server 8088:80
    ;;
  tester-install)
    kube apply -f https://raw.githubusercontent.com/susTuna/if4031-tester-agent/ebb55261711123a9a211739ffbf140a275caa6da/manifest.yaml
    ;;
  tester-list)
    kube exec -n tester-agent daemonset/tester-agent -- /app list
    ;;
  tester-practice)
    if [[ -z "${2:-}" ]]; then
      echo 'Pass the scenario name.' >&2
      exit 2
    fi
    kube exec -n tester-agent daemonset/tester-agent -- /app run practice "$2"
    ;;
  scale)
    if [[ ! "${2:-}" =~ ^[2-5]$ ]]; then
      echo 'Replica count must be 2..5.' >&2
      exit 2
    fi

    if [[ -n "$(kube get hpa aggregator -n "$namespace" --ignore-not-found -o name)" ]]; then
      echo 'Disable Aggregator HPA before manual scale tests.' >&2
      exit 1
    fi
    kube scale deployment/aggregator -n "$namespace" --replicas="$2"
    kube rollout status deployment/aggregator -n "$namespace" --timeout=180s
    ;;
  logs)
    kube logs -n "$namespace" -l "app.kubernetes.io/instance=${2:-aggregator}" \
      --all-containers --tail=100 --follow
    ;;
  *)
    echo 'task --list | task up | task deploy | task status | task links'
    ;;
esac
