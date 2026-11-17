#!/usr/bin/env python3
"""Validate the pinned binaries with isolated, disposable Docker containers."""
import argparse
from fnmatch import fnmatchcase
from pathlib import Path

import yaml
import subprocess
import tempfile
import time
import uuid
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def run(*args, timeout=180):
    return subprocess.run(args, capture_output=True, text=True, timeout=timeout, check=True)


def platform_applications():
    """Read the root's recursive directory selection without accessing a cluster."""
    source = yaml.safe_load((ROOT / "argo-apps/root.yaml").read_text())["spec"]["source"]
    folder = ROOT.parents[1] / source["path"]
    directory = source["directory"]
    # Root uses flat brace alternatives with *, **, and numeric character classes.
    patterns = directory["include"].removeprefix("{").removesuffix("}").split(",")
    paths = folder.rglob("*") if directory["recurse"] else folder.glob("*")
    for path in sorted(paths):
        if path.is_file() and path.suffix in (".yaml", ".yml", ".json"):
            relative = path.relative_to(folder).as_posix()
            if any(fnmatchcase(relative, pattern) for pattern in patterns):
                yield from (doc for doc in yaml.safe_load_all(path.read_text()) if doc)


def render_configs(folder, chart_cache=None):
    """Validate the configs actually produced by the registered Helm charts."""
    applications = platform_applications()
    configs = {
        "otel-collector-cluster": ("otel-collector-cluster", "relay", "collector.yaml"),
        "otel-collector-daemon": ("otel-collector-daemon-agent", "relay", "daemon.yaml"),
        "loki": ("loki", "config.yaml", "loki.yaml"),
        "tempo": ("tempo", "tempo.yaml", "tempo.yaml"),
        "mimir": ("mimir-config", "config.yaml", "mimir.yaml"),
    }
    for app in applications:
        if app["spec"]["destination"]["namespace"] != "monitoring":
            continue
        source = next(s for s in app["spec"].get("sources", [app["spec"].get("source", {})])
                      if "chart" in s or "path" in s)
        helm = source["helm"]
        command = ["helm", "template", helm["releaseName"]]
        if "chart" in source:
            if chart_cache:
                chart = chart_cache / source["chart"]
                version = yaml.safe_load((chart / "Chart.yaml").read_text())["version"]
                if version != source["targetRevision"]:
                    raise ValueError(f"Cached chart version differs for {app['metadata']['name']}")
                command.append(str(chart))
            else:
                command.extend([source["chart"], "--repo", source["repoURL"], "--version", source["targetRevision"]])
        else:
            command.append(str(ROOT.parents[1] / source["path"]))
        command.extend(["-n", "monitoring"])
        # Read resource expectations directly from the values used by this app.
        values = {}
        if "path" in source:
            values = yaml.safe_load((ROOT.parents[1] / source["path"] / "values.yaml").read_text())
        for value in helm.get("valueFiles", []):
            value_path = ROOT.parents[1] / value.removeprefix("$values/")
            command.extend(["-f", str(value_path)])
            values.update(yaml.safe_load(value_path.read_text()))
        name = app["metadata"]["name"]
        resource_path = {"loki": ("singleBinary", "resources"),
                         "tempo": ("tempo", "resources")}.get(name, ("resources",))
        expected = values
        for key in resource_path:
            expected = expected[key]
        documents = [d for d in yaml.safe_load_all(run(*command).stdout) if d]
        for doc in documents:
            if doc["kind"] not in ("Deployment", "StatefulSet", "DaemonSet"):
                continue
            pod = doc["spec"]["template"]
            for container in pod["spec"]["containers"]:
                if container.get("resources") != expected:
                    raise ValueError(f"Rendered resources differ from app values for {doc['metadata']['name']}")
        if name in configs:
            configmap, key, filename = configs[name]
            config = next(d["data"][key] for d in documents if d["kind"] == "ConfigMap" and d["metadata"]["name"] == configmap)
            (folder / filename).write_text(config)
        print(f"PASS: {name} upstream/local chart render and configured resources", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--chart-cache", type=Path, help="Optional directory of unpacked pinned upstream charts")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="aat-platform-config-") as folder:
        config = Path(folder)
        # Pinned images run as non-root and must traverse the read-only mount.
        config.chmod(0o755)
        render_configs(config, args.chart_cache)
        validate_configs(config)


def validate_configs(config):
    with tempfile.TemporaryDirectory(prefix="aat-config-fixture-") as folder:
        token = Path(folder) / "token"
        token.write_text("offline-validation-token\n")
        cases = [
            ("collector", "otel/opentelemetry-collector-contrib:0.160.0", ["validate", "--config=/etc/lgtm/collector.yaml"], []),
            ("daemon", "otel/opentelemetry-collector-contrib:0.160.0", ["validate", "--config=/etc/lgtm/daemon.yaml"], [
                "--tmpfs", "/var/lib/otelcol", "-v", f"{token}:/var/run/secrets/kubernetes.io/serviceaccount/token:ro",
                "-v", "/etc/ssl/certs/ca-certificates.crt:/var/run/secrets/kubernetes.io/serviceaccount/ca.crt:ro",
            ]),
            ("loki", "grafana/loki:3.6.3", ["-config.file=/etc/lgtm/loki.yaml", "-verify-config=true"], []),
            ("tempo", "grafana/tempo:2.9.0", ["-config.file=/etc/lgtm/tempo.yaml", "-config.verify=true"], []),
        ]
        for name, image, command, extra in cases:
            run("docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--memory", "256m",
                "-e", "NODE_NAME=aat-worker-1", "-e", "NODE_IP=127.0.0.1", "-v", f"{config}:/etc/lgtm:ro",
                *extra, image, *command)
            print(f"PASS: {name} configuration", flush=True)

    # Mimir has no verify-only flag in this version. Give it an eth0 interface,
    # as in a Pod, and ephemeral writable storage under the workload's UID.
    name = "aat-mimir-check-" + uuid.uuid4().hex[:8]
    try:
        run("docker", "run", "--detach", "--name", name, "--network", "bridge", "--read-only", "--cap-drop", "ALL",
            "--user", "10001:10001", "--memory", "1g", "--cpus", "1",
            "--tmpfs", "/data:uid=10001,gid=10001,mode=0770", "--tmpfs", "/tmp", "-w", "/data",
            "-v", f"{config}:/etc/lgtm:ro", "grafana/mimir:2.17.4", "-config.file=/etc/lgtm/mimir.yaml", "-target=all")
        address = run("docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name).stdout.strip()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        for attempt in range(45):
            logs = run("docker", "logs", name)
            output = logs.stdout + logs.stderr
            running = run("docker", "inspect", "--format", "{{.State.Running}}", name).stdout.strip()
            if running != "true":
                raise RuntimeError(output[-4000:])
            try:
                with opener.open(f"http://{address}:9009/ready", timeout=2) as response:
                    if response.status == 200:
                        print("PASS: Mimir startup and readiness", flush=True)
                        return
            except (urllib.error.URLError, TimeoutError):
                pass
            if attempt and attempt % 15 == 0:
                print("Waiting for Mimir readiness...", flush=True)
            time.sleep(2)
        raise RuntimeError("Mimir did not become ready within 90 seconds\n" + output[-4000:])
    finally:
        subprocess.run(["docker", "rm", "-f", name], capture_output=True)


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        raise SystemExit((error.stdout + error.stderr)[-4000:])
