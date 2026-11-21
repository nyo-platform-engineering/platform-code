"""Kubernetes topology, worker actions, and verified storage recovery."""
from contextlib import contextmanager
import base64
import importlib.util
import json
import shlex
import socket
import subprocess
import threading
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



class ShardsNotReady(RuntimeError):
    """A storage member or its routing metadata is still recovering."""


def shard_distribution(config, catalog_refs=None):
    """Wait for healthy cross-node copies, then inspect every canonical shard."""
    started = time.monotonic()
    timeout = getattr(config, "shard_ready_timeout", 180)
    while True:
        try:
            # Refresh pods and routing on every attempt: failover can change roles.
            return _shard_distribution(config, catalog_refs)
        except ShardsNotReady as error:
            remaining = timeout - (time.monotonic() - started)
            if remaining <= 0:
                raise TimeoutError(f"shard readiness did not recover within {timeout}s: {error}") from None
            output("WAIT", "Kesiapan shard", str(error),
                   f"Elapsed: {time.monotonic() - started:.0f}s / {timeout}s; belum melanjutkan tahap.")
            time.sleep(min(getattr(config, "sample_interval", 2), remaining))


def _shard_distribution(config, catalog_refs=None):
    """Inspect all shards once; only readiness failures are safe to poll again."""
    def ready(condition, message):
        if not condition:
            raise ShardsNotReady(message)

    pods = kubectl_json(config, "get", "pods", "-n", NAMESPACE,
                        "-l", "app.kubernetes.io/name=aat-citus", "-o", "json")["items"]

    def sql(pod, query):
        # Credentials stay inside the container; SQL and output contain no secrets.
        command = 'exec psql -X -h /tmp -U "$PATRONI_SUPERUSER_USERNAME" -d "$PATRONI_CITUS_DATABASE" -v ON_ERROR_STOP=1 -At -c "$1"'
        return json.loads(kubectl(config, "exec", pod, "-c", "postgres", "--",
                                  "sh", "-ec", command, "p1-shards", query, timeout=30))

    coordinators = [p for p in pods if p["metadata"].get("labels", {}).get("citus-group") == "0"
                    and p["metadata"]["labels"].get("patroni-role") == "primary" and is_ready(p)]
    ready(len(coordinators) == 1, "expected one Ready Citus coordinator")
    shards = sql(coordinators[0]["metadata"]["name"], """
        SELECT coalesce(json_agg(t ORDER BY shard_id), '[]'::json) FROM (
          SELECT s.shardid AS shard_id, p.groupid AS group_id, n.nodename AS primary_host
          FROM pg_dist_shard s JOIN pg_dist_placement p USING (shardid)
          JOIN pg_dist_node n USING (groupid)
          WHERE s.logicalrelid = 'hazard_events'::regclass AND n.noderole = 'primary'
        ) t""")
    require(len(shards) >= 2, "hazard_events must have multiple shards")
    require(len({s["shard_id"] for s in shards}) == len(shards), "expected one placement group per shard")
    groups = {s["group_id"] for s in shards}
    members = {}
    # Verify Patroni's live endpoint as well as Kubernetes labels/readiness.
    health_check = """import urllib.request,urllib.error,sys
try:
    with urllib.request.urlopen('http://127.0.0.1:8008/' + sys.argv[1], timeout=5) as response:
        print(response.status)
except urllib.error.HTTPError as error:
    print(error.code)
except (urllib.error.URLError, TimeoutError):
    print(0)
"""
    for pod in pods:
        labels = pod["metadata"].get("labels", {})
        group = int(labels.get("citus-group", "0"))
        role = labels.get("patroni-role")
        if group not in groups or role not in ("primary", "replica") or not is_ready(pod):
            continue
        path = 'primary' if role == 'primary' else 'replica?lag=0'
        status = kubectl(config, "exec", pod["metadata"]["name"], "-c", "postgres", "--",
                         "python3", "-c", health_check, path, timeout=15)
        status = int(status.strip())
        member_name = pod["metadata"]["name"]
        ready(status not in (0, 503), f"{member_name}: Patroni /{path} HTTP {status}; {role} belum siap")
        require(status == 200, f"{member_name}: unexpected Patroni /{path} HTTP {status}")
        members.setdefault(group, []).append({"node": pod["spec"]["nodeName"],
                                              "unit": pod["metadata"]["name"], "role": role})
    per_node = {p["spec"]["nodeName"]: {"primary_shards": [], "replica_shards": []} for p in pods}
    for group in sorted(groups):
        copies = members.get(group, [])
        primary = [m for m in copies if m["role"] == "primary"]
        ready(len(primary) == 1, f"group {group}: expected one ready primary")
        ready(any(m["role"] == "replica" and m["node"] != primary[0]["node"] for m in copies),
                f"group {group}: no ready replica on a different node")
        group_shards = [s for s in shards if s["group_id"] == group]
        ready(all(s["primary_host"].split('.')[0] == primary[0]["unit"] for s in group_shards),
                f"group {group}: Citus routing disagrees with Patroni primary")
        # Count actual rows on primary shards; replicas must not inflate the total.
        catalog_count = ""
        if catalog_refs is not None:
            require(bool(catalog_refs), "catalog references must not be empty")
            references = ",".join("'" + ref.replace("'", "''") + "'" for ref in sorted(catalog_refs))
            catalog_count = f", count(*) FILTER (WHERE source = 'BMKG' AND source_ref_id IN ({references})) AS catalog_events"
        selects = [f"SELECT {int(s['shard_id'])}::bigint AS shard_id, count(*) AS events{catalog_count} "
                   f"FROM public.hazard_events_{int(s['shard_id'])}" for s in group_shards]
        counts = sql(primary[0]["unit"], "SELECT json_agg(t) FROM (" + " UNION ALL ".join(selects) + ") t")
        counts = {row["shard_id"]: row for row in counts}
        for shard in group_shards:
            shard.update(counts[shard["shard_id"]])
            shard["locations"] = copies
            for member in copies:
                per_node[member["node"]][member["role"] + "_shards"].append(shard["shard_id"])
    if catalog_refs is not None:
        catalog_total = sum(s["catalog_events"] for s in shards)
        require(catalog_total == len(catalog_refs), "shard counts do not cover the catalog")
        require(all(s["catalog_events"] < catalog_total for s in shards), "one shard holds the whole catalog")
        catalog_primary_nodes = {m["node"] for s in shards if s["catalog_events"] > 0
                                 for m in s["locations"] if m["role"] == "primary"}
        require(len(catalog_primary_nodes) >= 2, "catalog primary writes are concentrated on one node")
    total = sum(s["events"] for s in shards)
    require(total > 0 and all(s["events"] < total for s in shards), "one shard holds the whole dataset")
    require(sum(bool(n["primary_shards"]) for n in per_node.values()) >= 2,
            "primary shards are concentrated on one node")
    return {"at": utc(), "scope": "all_hazard_events_shards", "events": total,
            "shards": shards, "shards_per_node": per_node, "redundancy_checked": True}


def cluster_metrics(config):
    node_path = "/apis/metrics.k8s.io/v1beta1/nodes"
    pod_path = f"/apis/metrics.k8s.io/v1beta1/namespaces/{NAMESPACE}/pods"
    return {
        "at": utc(),
        "topology": cluster_topology(config),
        "nodes": kubectl_json(config, "get", "--raw", node_path),
        "pods": kubectl_json(config, "get", "--raw", pod_path),
    }


def select_surviving_aggregator(topology, excluded_nodes=()):
    ready_nodes = {node["name"] for node in topology["nodes"]
                   if node["ready"] and node["name"] not in excluded_nodes}
    control_nodes = {node["name"] for node in topology["nodes"] if node["control_plane"]}
    candidates = [pod for pod in topology["pods"]
                  if pod["role"] == "aggregator" and pod["ready"] and pod["node"] in ready_nodes]
    # Prefer the control plane, but a healthy worker can serve the same API.
    candidates.sort(key=lambda pod: pod["node"] not in control_nodes)
    if candidates:
        return candidates[0]["name"]
    raise ValueError("no Ready Aggregator on a surviving node")


def wait_for_port(process, port, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        require(process.poll() is None, "port-forward exited; see port-forward.log")
        try:
            with socket.create_connection(
                ("127.0.0.1", port), timeout=min(0.2, max(0.001, deadline - time.monotonic())),
            ):
                return
        except OSError:
            time.sleep(min(0.1, max(0, deadline - time.monotonic())))
    raise TimeoutError("Aggregator port-forward did not become ready within the request deadline")


@contextmanager
def endpoint(config, folder):
    """Reconnect a dead kubectl tunnel without retrying application failures."""
    if config.url:
        yield
        return

    topology = cluster_topology(config)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    config.url = f"http://127.0.0.1:{port}"
    config.endpoint_generation = 0
    config.endpoint_reconnections = []
    config.endpoint_connection_attempts = []
    config.endpoint_node = None
    process = None
    lock = threading.Lock()

    with (folder / "port-forward.log").open("a") as log:
        def ensure_forward(timeout, failed_generation=None):
            nonlocal process
            deadline = time.monotonic() + timeout
            if not lock.acquire(timeout=max(0, timeout)):
                raise TimeoutError("timed out waiting for the Aggregator tunnel")
            attempt = None
            try:
                excluded = set(getattr(config, "endpoint_excluded_nodes", ()))
                unsafe = config.endpoint_node in excluded
                # Another probe may already have replaced the failed tunnel.
                if failed_generation is not None and failed_generation != config.endpoint_generation and not unsafe:
                    return True
                if process is not None and process.poll() is None and not unsafe:
                    if failed_generation is None:
                        return False
                    try:
                        process.wait(timeout=min(0.2, max(0, deadline - time.monotonic())))
                    except subprocess.TimeoutExpired:
                        return False  # A live tunnel is not an application-error retry policy.

                attempt = {"at": utc(), "excluded_nodes": sorted(excluded), "status": "connecting",
                           "reason": "avoid_target_worker" if unsafe else "initial" if process is None else "tunnel_exited"}
                config.endpoint_connection_attempts.append(attempt)
                if unsafe and process is not None and process.poll() is None:
                    output("CONNECTION", "Pindahkan tunnel sebelum worker dimatikan",
                           f"Worker {config.endpoint_node} menjadi target gangguan; pilih node lain sebelum probe dimulai.")
                    process.terminate()
                    try:
                        process.wait(timeout=min(1, max(0, deadline - time.monotonic())))
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()

                waiting_reported = False
                while True:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise TimeoutError(f"no Ready Aggregator on a surviving node within the request deadline; "
                                           f"excluded={sorted(excluded)}; see {folder / 'endpoint-connections.json'}")
                    pods = kubectl(
                        config, "get", "pods", "-o", "json", json_output=True,
                        timeout=remaining, request_timeout=f"{remaining:.3f}s",
                    )["items"]
                    # Never select terminating pods or a worker targeted by the loss test.
                    candidates = [{
                        "name": pod["metadata"]["name"],
                        "node": pod["spec"].get("nodeName"),
                        "role": pod["metadata"].get("labels", {}).get("role"),
                        "ready": is_ready(pod),
                    } for pod in pods if not pod["metadata"].get("deletionTimestamp")]
                    try:
                        attempt["candidates"] = [pod for pod in candidates if pod["role"] == "aggregator"]
                        pod_name = select_surviving_aggregator(
                            {"nodes": topology["nodes"], "pods": candidates}, excluded,
                        )
                        node_name = next(pod["node"] for pod in candidates if pod["name"] == pod_name)
                        attempt.update(pod=pod_name, node=node_name)
                        break
                    except ValueError:
                        if not waiting_reported:
                            output("CONNECTION", "Menunggu Aggregator pengganti",
                                   "Belum ada pod Ready pada node yang boleh dipakai; menunggu dalam sisa batas waktu request.")
                            waiting_reported = True
                        time.sleep(min(0.2, max(0, deadline - time.monotonic())))
                command = [
                    "kubectl", "--kubeconfig", str(config.kubeconfig), "-n", NAMESPACE,
                    "port-forward", "pod/" + pod_name, f"{port}:8083", "--address=127.0.0.1",
                ]
                process = subprocess.Popen(command, stdout=log, stderr=log)
                wait_for_port(process, port, timeout=max(0, deadline - time.monotonic()))
                config.endpoint_generation += 1
                config.endpoint_node = node_name
                attempt["status"] = "connected"
                if config.endpoint_generation > 1:
                    config.endpoint_reconnections.append({"at": utc(), "pod": pod_name, "node": node_name,
                                                          "reason": attempt["reason"]})
                    output("CONNECTION", "Tunnel Aggregator tersambung kembali",
                           f"Pod tujuan: {pod_name} pada {node_name}; alamat lokal tetap {config.url}",
                           "Tunnel dipindahkan dari worker target sebelum gangguan." if unsafe else
                           "Tunnel kubectl sebelumnya berhenti; kegagalan transport tetap dicatat.",
                           "HTTP 500, timeout, dan kegagalan validasi data tidak diulang.")
                return True
            except subprocess.TimeoutExpired:
                message = (f"Aggregator tunnel: kubectl timed out within the remaining {timeout:.2f}s budget; "
                           f"see {folder / 'endpoint-connections.json'} and port-forward.log")
                if attempt is not None:
                    attempt.update(status="failed", error=message)
                raise TimeoutError(message) from None
            except Exception as error:
                if attempt is not None:
                    attempt.update(status="failed", error=str(error) or type(error).__name__)
                raise
            finally:
                try:
                    if attempt is not None:
                        (folder / "endpoint-connections.json").write_text(
                            json.dumps(config.endpoint_connection_attempts, indent=2) + "\n",
                        )
                finally:
                    lock.release()

        config.ensure_endpoint = ensure_forward
        try:
            ensure_forward(30)
            yield
        finally:
            del config.ensure_endpoint
            if process is not None and process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            config.url = None


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


def polling_freeze_module():
    script = INFRA / "scripts/tester/freeze-polling.py"
    spec = importlib.util.spec_from_file_location("p1_polling_freeze", script)
    polling = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(polling)
    return polling


@contextmanager
def frozen_mock_checkpoint(run, name):
    """Pause only mock fetches for a quiet checkpoint, never inside a live monitor."""
    require(not getattr(run, "active_monitors", 0),
            "mock polling freeze cannot run inside a load/recovery monitor")
    require(run.config.kubeconfig.resolve() == (INFRA / ".state/kubeconfig").resolve(),
            "automatic mock freeze only supports the local lab kubeconfig")
    polling = polling_freeze_module()
    record = {"checkpoint": name, "started_at": utc(), "sources": ["BMKG", "PVMBG"],
              "scope": "upstream fetches only; other sources and direct writes remain live"}
    records = run.report.setdefault("polling_freezes", [])
    records.append(record)
    entered = False
    try:
        output("CHECKPOINT", name, "P1 pauses BMKG/PVMBG polling automatically for this comparison.",
               "Polling resumes before continuing; no separate polling:freeze task is needed.")
        with polling.paused_polling():
            entered = True
            ensure = getattr(run.config, "ensure_endpoint", None)
            if ensure:
                ensure(30)
            record["frozen_at"] = utc()
            run.mock_polling_frozen = True
            try:
                yield
            finally:
                run.mock_polling_frozen = False
        if ensure:
            ensure(30)
        record["status"] = "completed"
    except BaseException:
        record["status"] = "failed"
        raise
    finally:
        run.mock_polling_frozen = False
        record["restored"] = entered and not polling.STATE.exists()
        record["finished_at"] = utc()
        run.save("polling-freezes.json", records)
        if record["restored"]:
            output("CHECKPOINT", name, "Mock polling restored; subsequent load/recovery steps remain live.")
