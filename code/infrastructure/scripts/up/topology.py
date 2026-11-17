#!/usr/bin/env python3
"""Preflight only: API metadata cannot prove that guests have separate kernels."""
import json
import sys


def validate(nodes):
    if len(nodes) < 3:
        raise ValueError("Use >=3 VM/native nodes for this local topology (three Aggregators and P1 storage work).")
    for node in nodes:
        info = node["status"]["nodeInfo"]
        name = node["metadata"]["name"]
        if name.startswith(("k3d-", "kind-", "minikube")) or info.get("osImage", "").startswith("K3s "):
            raise ValueError("Container-node development clusters do not satisfy the demo's separate-kernel requirement.")
    return True


if __name__ == "__main__":
    try:
        validate(json.load(sys.stdin)["items"])
        print("Topology preflight passed; record per-VM boot IDs and host specifications for the report.")
    except (ValueError, KeyError) as error:
        sys.exit(str(error))
