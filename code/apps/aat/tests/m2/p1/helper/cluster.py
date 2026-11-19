"""Kubernetes topology, worker actions, and verified storage recovery."""
from contextlib import contextmanager
import base64
import json
import shlex
import socket
import subprocess
import time

from common import INFRA, NAMESPACE, kubectl, output, require, utc


CONTROL_PLANE_LABELS = (
    "node-role.kubernetes.io/control-plane",
    "node-role.kubernetes.io/master",
)


def is_ready(resource):
    conditions = resource["status"].get("conditions", [])
    return any(item["type"] == "Ready" and item["status"] == "True" for item in conditions)


def kubectl_json(config, *command):
    return kubectl(config, *command, json_output=True, timeout=15,
                   request_timeout="10s", namespace=None)


def cluster_topology(config):
    nodes = kubectl_json(config, "get", "nodes", "-o", "json")["items"]
    pods = kubectl_json(config, "get", "pods", "-n", NAMESPACE, "-o", "json")["items"]
    # Retain placement/health, without pod env values or Secret references.
    return {
        "at": utc(),
        "nodes": [{
            "name": node["metadata"]["name"],
            "control_plane": any(label in node["metadata"].get("labels", {})
                                 for label in CONTROL_PLANE_LABELS),
            "ready": is_ready(node),
            "kernel": node["status"].get("nodeInfo", {}).get("kernelVersion"),
            "provider_id": node["spec"].get("providerID"),
        } for node in nodes],
        "pods": [{
            "name": pod["metadata"]["name"],
            "uid": pod["metadata"]["uid"],
            "node": pod["spec"].get("nodeName"),
            "role": pod["metadata"].get("labels", {}).get("role"),
            "ready": is_ready(pod),
        } for pod in pods],
    }


def cluster_metrics(config):
    node_path = "/apis/metrics.k8s.io/v1beta1/nodes"
    pod_path = f"/apis/metrics.k8s.io/v1beta1/namespaces/{NAMESPACE}/pods"
    return {
        "at": utc(),
        "topology": cluster_topology(config),
        "nodes": kubectl_json(config, "get", "--raw", node_path),
        "pods": kubectl_json(config, "get", "--raw", pod_path),
    }


def select_surviving_aggregator(topology):
    control_nodes = {
        node["name"] for node in topology["nodes"]
        if node["control_plane"] and node["ready"]
    }
    for pod in topology["pods"]:
        if pod["role"] == "aggregator" and pod["ready"] and pod["node"] in control_nodes:
            return pod["name"]
    raise ValueError("no Ready Aggregator on control plane; set CONFIG.url to a stable Aggregator endpoint")


def wait_for_port(process, port):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        require(process.poll() is None, "port-forward exited; see port-forward.log")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return
        except OSError:
            time.sleep(0.1)
    raise ValueError("port-forward did not become ready")


@contextmanager
def endpoint(config, folder):
    """Keep the chosen Aggregator reachable while workers are disrupted."""
    if config.url:
        yield
        return

    pod_name = select_surviving_aggregator(cluster_topology(config))
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    config.url = f"http://127.0.0.1:{port}"

    command = [
        "kubectl", "--kubeconfig", str(config.kubeconfig), "-n", NAMESPACE,
        "port-forward", "pod/" + pod_name, f"{port}:8083", "--address=127.0.0.1",
    ]
    with (folder / "port-forward.log").open("w") as log:
        process = subprocess.Popen(command, stdout=log, stderr=log)
        try:
            wait_for_port(process, port)
            yield
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def worker_action_driver(config, worker):
    """Only automate the known local lab and its two non-control-plane workers."""
    require(worker in ("aat-worker-1", "aat-worker-2"), "automatic actions only support the two lab workers")
    require(config.kubeconfig.resolve() == (INFRA / ".state/kubeconfig").resolve(),
            "automatic worker actions require the local infrastructure kubeconfig")
    mode_file = INFRA / ".state/mode"
    if mode_file.is_file() and mode_file.read_text().strip() == "k3d":
        return "k3d-development-only"
    machine_id = INFRA / "local/.vagrant/machines" / worker / "libvirt/id"
    require(machine_id.is_file() and machine_id.read_text().strip(),
            f"local Vagrant/libvirt identity missing for {worker}; use manual worker actions for another cluster")
    return "vagrant-libvirt"


def storage_plan(run, worker):
    """Resolve only this worker's bound AAT store PVCs, with healthy peers."""
    require(worker_action_driver(run.config, worker) == "vagrant-libvirt",
            "automatic storage erasure requires the local Vagrant VM lab")
    pods = kubectl_json(run.config, "get", "pods", "-n", NAMESPACE, "-l", "role=store", "-o", "json")["items"]
    nodes = cluster_topology(run.config)["nodes"]
    require(any(n["name"] == worker and n["ready"] and not n["control_plane"] for n in nodes),
            "storage loss requires a Ready non-control-plane worker")
    ready_nodes = {n["name"] for n in nodes if n["ready"]}
    selected = [p for p in pods if p["spec"].get("nodeName") == worker]
    require(bool(selected), "no store pods found on selected worker")
    volumes = []
    for pod in selected:
        labels = pod["metadata"]["labels"]
        group = labels.get("citus-group")
        require(labels.get("app.kubernetes.io/name") == "aat-citus" and group in ("1", "2", "3"),
                "unexpected store workload; refusing storage erasure")
        peers = [p for p in pods if p["metadata"]["labels"].get("citus-group") == group
                 and p["spec"].get("nodeName") != worker
                 and p["spec"].get("nodeName") in ready_nodes and is_ready(p)]
        require(bool(peers) and is_ready(pod), f"Citus group {group} needs a healthy replica before loss")
        claims = [v["persistentVolumeClaim"]["claimName"] for v in pod["spec"]["volumes"]
                  if "persistentVolumeClaim" in v]
        require(claims == ["data-" + pod["metadata"]["name"]], "unexpected store PVC layout")
        claim = kubectl_json(run.config, "get", "pvc", claims[0], "-n", NAMESPACE, "-o", "json")
        pv = kubectl_json(run.config, "get", "pv", claim["spec"]["volumeName"], "-o", "json")
        ref = pv["spec"]["claimRef"]
        require(claim["status"]["phase"] == "Bound" and ref["uid"] == claim["metadata"]["uid"]
                and ref["namespace"] == NAMESPACE and ref["name"] == claims[0], "PVC/PV identity mismatch")
        terms = pv["spec"]["nodeAffinity"]["required"]["nodeSelectorTerms"]
        require(len(terms) == 1 and terms[0]["matchExpressions"] == [
            {"key": "kubernetes.io/hostname", "operator": "In", "values": [worker]}],
            "PV is not restricted to the selected worker")
        path = pv["spec"].get("local", pv["spec"].get("hostPath", {})).get("path")
        expected = f"/var/lib/rancher/k3s/storage/pvc-{ref['uid']}_{NAMESPACE}_{ref['name']}"
        require(path == expected, "unexpected local store data path")
        volumes.append({"pod": pod["metadata"]["name"], "claim": ref["name"], "path": path})
    require(len({v["path"] for v in volumes}) == len(volumes), "duplicate store volume")
    machine = INFRA / "local/.vagrant/machines" / worker / "libvirt/id"
    return {"worker": worker, "uuid": machine.read_text().strip(), "volumes": volumes}


STORAGE_SCRIPT = r"""
import base64, json, os, pathlib, shutil, socket, subprocess, sys, time
plan = json.loads(base64.b64decode(sys.argv[1]))
operation = sys.argv[2]
assert operation in ('erase', 'inspect')
assert os.geteuid() == 0
assert socket.gethostname() == plan['worker']
assert pathlib.Path('/sys/class/dmi/id/product_uuid').read_text().strip().lower() == plan['uuid'].lower()
root = pathlib.Path('/var/lib/rancher/k3s/storage')
assert root.resolve() == root
paths = []
for volume in plan['volumes']:
    path = pathlib.Path(volume['path'])
    assert path.parent == root and path.resolve() == path and path.is_dir()
    assert path.name.startswith('pvc-') and path.name.endswith('_' + volume['claim'])
    assert not os.path.ismount(path)
    paths.append((volume, path))
assert paths
if operation == 'erase':
    subprocess.run(['systemctl', 'stop', 'k3s-agent'], check=True)
    subprocess.run(['/usr/local/bin/k3s-killall.sh'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    assert subprocess.run(['systemctl', 'is-active', '--quiet', 'k3s-agent']).returncode != 0
    assert subprocess.run(['pgrep', '-x', 'postgres'], stdout=subprocess.DEVNULL).returncode == 1
    mounts = [pathlib.Path(line.split()[4]) for line in pathlib.Path('/proc/self/mountinfo').read_text().splitlines()]
    for volume, path in paths:
        assert path.resolve() == path
        assert not any(m == path or path in m.parents for m in mounts)
results = []
for volume, path in paths:
    if operation == 'erase':
        files = [p for p in path.rglob('*') if p.is_file() and not p.is_symlink()]
        size = sum(p.stat().st_size for p in files)
        count = len(files)
        for child in path.iterdir():
            if child.is_symlink() or not child.is_dir():
                child.unlink()
            else:
                shutil.rmtree(child)
        assert not list(path.iterdir())
        results.append(dict(volume, files_before=count, bytes_before=size, entries_after=0))
    else:
        version = path / 'pgdata/PG_VERSION'
        results.append(dict(volume, rebuilt=version.is_file(), entries_after=len(list(path.iterdir()))))
os.sync()
print(json.dumps(dict(worker=plan['worker'], operation=operation, at=time.time(), volumes=results)))
"""


def worker_storage(run, name, plan, operation):
    payload = base64.b64encode(json.dumps(plan).encode()).decode()
    remote = "sudo python3 -I -c " + shlex.quote(STORAGE_SCRIPT) + " " + shlex.quote(payload) + " " + operation
    completed = subprocess.run(["vagrant", "ssh", plan["worker"], "--no-tty", "-c", remote],
                               cwd=INFRA / "local", text=True, capture_output=True, timeout=120)
    (run.folder / f"{name}-storage-{operation}.log").write_text(completed.stdout + completed.stderr)
    require(completed.returncode == 0, f"worker storage {operation} failed; see storage log")
    proof = json.loads(completed.stdout.strip().splitlines()[-1])
    run.save(f"{name}-storage-{operation}.json", proof)
    return proof


def wait_for_recovery(run, name, redundant):
    from .data import DataUnavailable, capture_snapshot

    started = time.monotonic()
    attempt = 0
    while True:
        attempt += 1
        try:
            output("PROGRESS", name, f"Recovery check: {attempt}", f"Elapsed: {time.monotonic() - started:.0f}s")
            return capture_snapshot(run, name, redundant)
        except DataUnavailable as error:
            output("WAIT", "Waiting", f"{name}: {error}")
            time.sleep(run.config.sample_interval)


def run_worker_action(run, name, worker, rejoin):
    """Run the local lab's allowlisted power action after storage preparation."""
    driver = worker_action_driver(run.config, worker)
    node = next((node for node in cluster_topology(run.config)["nodes"] if node["name"] == worker), None)
    require(node is not None and not node["control_plane"], "refusing to power off the control plane or an unknown node")
    action = "start-worker" if rejoin else "stop-worker"
    command = ["bash", str(INFRA / "scripts/vm/main.sh"), action, worker]
    record = {"at": utc(), "worker": worker, "action": action, "driver": driver,
              "action_scope": "power_only", "status": "started"}
    actions = run.report.setdefault("worker_actions", [])
    actions.append(record)
    log_path = run.folder / f"{name}-worker-action.log"
    process = None
    output("ACTION", "Worker action", f"{action} {worker} via {driver}")
    try:
        with log_path.open("w") as log:
            process = subprocess.Popen(command, cwd=INFRA, stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT, text=True, bufsize=1)
            for line in process.stdout:
                log.write(line)
                log.flush()
                if line.strip():
                    output("VM", "VM output", line.rstrip())
            require(process.wait() == 0, f"{action} failed; see {log_path}")
        record["status"] = "completed"
    except BaseException as error:
        record["status"] = "interrupted" if isinstance(error, KeyboardInterrupt) else "failed"
        raise
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        record["finished_at"] = utc()
        run.save("worker-actions.json", actions)


def wait_for_node_state(run, monitor, worker, ready):
    started = time.monotonic()
    expected = "Ready" if ready else "NotReady"
    last_state, last_print = None, 0
    while True:
        nodes = cluster_topology(run.config)["nodes"]
        node = next((node for node in nodes if node["name"] == worker), None)
        observed = "missing" if node is None else "Ready" if node["ready"] else "NotReady"
        if observed != last_state or time.monotonic() - last_print >= 10:
            output("PROGRESS", worker, f"Observed: {observed}; waiting for: {expected}",
                   f"Elapsed: {time.monotonic() - started:.0f}s")
            last_state, last_print = observed, time.monotonic()
        if node is not None and node["ready"] == ready:
            monitor.target_seen = True
            monitor.target_ready = ready
            run.save(f"{monitor.name}-observed-node.json", {"at": utc(), "node": node})
            return
        time.sleep(run.config.sample_interval)


@contextmanager
def restore_worker_on_failure(run, name, worker):
    """Restart the lab worker if an automatic loss/rejoin check is interrupted."""
    try:
        yield
    except BaseException:
        if not run.config.automatic_worker_actions:
            raise
        # Leave the failed result intact, but bring this lab worker back so
        # an interrupted erase cannot strand its surviving peer indefinitely.
        try:
            output("RECOVERY", "Worker recovery", f"restarting {worker} after the failed/interrupted check")
            run_worker_action(run, name + "-cleanup", worker, rejoin=True)
            deadline = time.monotonic() + 60
            while True:
                recovery = subprocess.run(
                    ["vagrant", "ssh", worker, "--no-tty", "-c", "sudo systemctl start k3s-agent"],
                    cwd=INFRA / "local", capture_output=True, text=True, timeout=20,
                )
                (run.folder / f"{name}-cleanup.log").write_text(recovery.stdout + recovery.stderr)
                if recovery.returncode == 0:
                    output("RECOVERY", "Worker recovery", f"{worker} started; test remains failed/interrupted")
                    break
                require(time.monotonic() < deadline, "worker SSH not ready for cleanup")
                time.sleep(2)
        except Exception:
            output("INFO", "Information", f"Restore {worker}: bash scripts/vm/main.sh start-worker {worker}; "
                  f"then from infrastructure/local: vagrant ssh {worker} "
                  "-c 'sudo systemctl start k3s-agent'")
        raise
