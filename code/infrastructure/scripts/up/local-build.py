#!/usr/bin/env python3
"""Build native images for the private k3d lab and retain them through GitOps sync."""
import argparse
import copy
import json
import os
from pathlib import Path
import re
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
SERVICES = {
    "aggregator": "aggregator/Dockerfile",
    "auth": "auth/Dockerfile",
    "bmkg": "bmkg/Dockerfile",
    "client": "client/Dockerfile",
    "dashboard-updater": "consumers/dashboard-updater/Dockerfile",
    "field-notifier": "consumers/field-notifier/Dockerfile",
    "pvmbg": "pvmbg/Dockerfile",
    "citus": "storage/postgres/Dockerfile.local",
}


def image_plan(cluster, architecture, tag):
    if architecture not in {"amd64", "arm64"}:
        raise ValueError(f"Unsupported node architecture: {architecture}")
    if not re.fullmatch(r"local-[a-f0-9]{12}", tag):
        raise ValueError("Invalid local image tag")
    return {
        "cluster": cluster,
        "architecture": architecture,
        "tag": tag,
        "images": {name: f"docker.io/aat-local/{name}:{tag}" for name in SERVICES},
    }


def applications(root, dev, plan):
    root, dev = copy.deepcopy(root), copy.deepcopy(dev)
    rule = {"group": "argoproj.io", "kind": "Application", "name": "dev-apps",
            "namespace": "argocd", "jsonPointers": ["/spec/source/kustomize"]}
    ignores = root["spec"].setdefault("ignoreDifferences", [])
    if rule not in ignores:
        ignores.append(rule)
    options = root["spec"]["syncPolicy"].setdefault("syncOptions", [])
    if "RespectIgnoreDifferences=true" not in options:
        options.append("RespectIgnoreDifferences=true")
    patches = []
    for service in SERVICES:
        app = "dependencies" if service == "citus" else service
        parameters = [
            {"name": "image.repository", "value": f"docker.io/aat-local/{service}", "forceString": True},
            {"name": "image.tag", "value": plan["tag"], "forceString": True},
            {"name": "image.digest", "value": "", "forceString": True},
            {"name": "image.pullPolicy", "value": "Never", "forceString": True},
        ]
        patches.append({
            "target": {"group": "argoproj.io", "version": "v1alpha1", "kind": "Application", "name": f"aat-{app}-dev"},
            "patch": json.dumps([{"op": "add", "path": "/spec/source/helm/parameters", "value": parameters}]),
        })
    dev["spec"]["source"]["kustomize"] = {"patches": patches}
    return root, dev


class LocalBuild:
    def __init__(self):
        self.cluster = os.environ.get("AAT_K3D_CLUSTER", "aat")
        self.state = ROOT / ".state" / "local-build"
        self.kubeconfig = ROOT / ".state" / "kubeconfig"

    def kube(self, *args, **kwargs):
        return subprocess.run(["kubectl", "--kubeconfig", str(self.kubeconfig), *args],
                              check=True, text=True, **kwargs)

    def validate_cluster(self):
        if (ROOT / ".state" / "mode").read_text().strip() != "k3d":
            raise ValueError("Local builds require the k3d lab (--novm)")
        context = self.kube("config", "current-context", capture_output=True).stdout.strip()
        if context != f"k3d-{self.cluster}":
            raise ValueError("Kubeconfig does not select the expected private k3d cluster")
        nodes = json.loads(self.kube("get", "nodes", "-o", "json", capture_output=True).stdout)["items"]
        if {node["metadata"]["name"] for node in nodes} != {"aat-control", "aat-worker-1", "aat-worker-2"}:
            raise ValueError("Expected the three AAT k3d nodes")
        architectures = {node["status"]["nodeInfo"]["architecture"] for node in nodes}
        if len(architectures) != 1:
            raise ValueError("Local builds require one common node architecture")
        return architectures.pop()

    def build(self):
        architecture = self.validate_cluster()
        plan = image_plan(self.cluster, architecture, "local-" + uuid.uuid4().hex[:12])
        source = ROOT.parent / "apps" / "aat"
        for service, dockerfile in SERVICES.items():
            subprocess.run(["docker", "build", "--platform", f"linux/{architecture}",
                            "--file", str(source / dockerfile), "--tag", plan["images"][service], str(source)], check=True)
        with (source / "storage" / "postgres" / "smoke-test.sh").open() as smoke:
            subprocess.run(["docker", "run", "--rm", "--platform", f"linux/{architecture}", "-i",
                            "--entrypoint", "/bin/sh", plan["images"]["citus"], "-s"], stdin=smoke, check=True)
        subprocess.run(["k3d", "image", "import", "--cluster", self.cluster, *plan["images"].values()], check=True)
        self.state.mkdir(parents=True, exist_ok=True, mode=0o700)
        temporary = self.state / "images.json.tmp"
        temporary.write_text(json.dumps(plan, indent=2) + "\n")
        temporary.replace(self.state / "images.json")
        print(f"Imported native {architecture} images; tag {plan['tag']}")

    def load_application(self, path):
        output = self.kube("create", "--dry-run=client", "--validate=false", "-f", str(ROOT / path),
                           "-o", "json", capture_output=True).stdout
        return json.loads(output)

    def register(self):
        architecture = self.validate_cluster()
        plan = json.loads((self.state / "images.json").read_text())
        if plan != image_plan(self.cluster, architecture, plan["tag"]):
            raise ValueError("Local image state belongs to a different cluster/architecture or is invalid")
        root, dev = applications(self.load_application("argo-apps/root.yaml"),
                                 self.load_application("argo-apps/platform/05-environments/05-dev-apps.yaml"), plan)
        self.kube("apply", "-f", "-", input=json.dumps(root))
        # Let any sync started with the previous root settings finish first.
        deadline = time.monotonic() + 180
        while True:
            live = json.loads(self.kube("get", "application", "platform", "-n", "argocd", "-o", "json", capture_output=True).stdout)
            running = live.get("status", {}).get("operationState", {}).get("phase") == "Running"
            if not running and not live.get("operation"):
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("Root sync is still running; retry startup after it finishes")
            time.sleep(2)
        self.kube("apply", "-f", "-", input=json.dumps(dev))
        print(f"Registered local image overrides ({plan['tag']}); shared GitOps pins are unchanged")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["build", "register"])
    args = parser.parse_args()
    builder = LocalBuild()
    getattr(builder, args.action)()


if __name__ == "__main__":
    main()
