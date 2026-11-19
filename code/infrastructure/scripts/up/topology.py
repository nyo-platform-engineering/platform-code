#!/usr/bin/env python3
"""Preflight only: API metadata cannot prove that guests have separate kernels."""
import json
import os
import sys


def validate(nodes, allow_containers=False):
    if len(nodes) < 3:
        raise ValueError("Use >=3 VM/native nodes with separate kernels for this local topology.")
    if allow_containers:
        # task up -- --novm: k3d nodes are containers sharing the host kernel.
        return True
    for node in nodes:
        info = node["status"]["nodeInfo"]
        name = node["metadata"]["name"]
        if name.startswith(("k3d-", "kind-", "minikube")) or info.get("osImage", "").startswith("K3s "):
            raise ValueError("Container-node development clusters do not satisfy the demo's separate-kernel requirement.")
    return True


if __name__ == "__main__":
    try:
        novm = os.environ.get("AAT_NOVM") == "1"
        validate(json.load(sys.stdin)["items"], allow_containers=novm)
        if novm:
            print("Topology preflight passed in k3d mode (development only): nodes share one kernel "
                  "and do not satisfy the demo requirement.")
        else:
            print("Topology preflight passed; record per-VM boot IDs and host specifications for the report.")
    except (ValueError, KeyError) as error:
        sys.exit(str(error))
