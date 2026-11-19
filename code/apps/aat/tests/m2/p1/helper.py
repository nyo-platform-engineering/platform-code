"""Shared P1 setup, Kubernetes access, HTTP evidence, and run state."""
from contextlib import contextmanager
import base64
import shlex
import datetime as dt
import importlib.util
import json
from pathlib import Path
import socket
import subprocess
import sys
import threading
import time
import textwrap

ROOT = Path(__file__).resolve().parents[3]
INFRA = ROOT.parents[1] / "infrastructure"
NAMESPACE = "sistem-informasi-kebencanaan-bnpb"
sys.path.insert(0, str(ROOT / "scripts"))
from analytics_http import API, checksum


class ConcurrentDataChange(ValueError):
    """An exact comparison must be retried because its selected data changed."""


def load_script(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def require(condition, message):
    if not condition:
        raise ValueError(message)


OUTPUT_LOCK = threading.Lock()


def output(kind, title, *details):
    """Print each title and its indented details together across monitor threads."""
    lines = [f"[{kind}] {title}"]
    for detail in details:
        for line in str(detail).splitlines():
            if line:
                lines.append(textwrap.fill(line, width=100, initial_indent="  ",
                                           subsequent_indent="  ", break_long_words=False,
                                           break_on_hyphens=False))
    with OUTPUT_LOCK:
        print("\n".join(lines), flush=True)


def progress(label, current, total=None, started=None, detail=""):
    count = str(current) if total is None else f"{current}/{total} ({current / total:.0%})"
    details = [f"Completed: {count}"]
    if started is not None:
        details.append(f"Elapsed: {time.monotonic() - started:.0f}s")
    if detail:
        details.extend(part.strip() for part in detail.split("; "))
    output("PROGRESS", label, *details)


analytics = load_script("p1_analytics", "verify-analytics.py")
catalog = load_script("p1_catalog", "import-catalog.py")


CONTROL_PLANE_LABELS = (
    "node-role.kubernetes.io/control-plane",
    "node-role.kubernetes.io/master",
)


def is_ready(resource):
    conditions = resource["status"].get("conditions", [])
    return any(item["type"] == "Ready" and item["status"] == "True" for item in conditions)


def describe_node(node):
    labels = node["metadata"].get("labels", {})
    return {
        "name": node["metadata"]["name"],
        "control_plane": any(label in labels for label in CONTROL_PLANE_LABELS),
        "ready": is_ready(node),
        "kernel": node["status"].get("nodeInfo", {}).get("kernelVersion"),
        "provider_id": node["spec"].get("providerID"),
    }


def describe_pod(pod):
    return {
        "name": pod["metadata"]["name"],
        "uid": pod["metadata"]["uid"],
        "node": pod["spec"].get("nodeName"),
        "role": pod["metadata"].get("labels", {}).get("role"),
        "ready": is_ready(pod),
    }


class Cluster:
    def __init__(self, config):
        self.config = config

    def get(self, *command):
        completed = subprocess.run(
            ["kubectl", "--kubeconfig", str(self.config.kubeconfig),
             "--request-timeout=10s", *command],
            capture_output=True,
            text=True,
            timeout=15,
        )
        require(completed.returncode == 0, "kubectl failed: " + " ".join(command[:3]))
        return json.loads(completed.stdout)

    def topology(self):
        nodes = self.get("get", "nodes", "-o", "json")["items"]
        pods = self.get("get", "pods", "-n", NAMESPACE, "-o", "json")["items"]
        # Retain placement/health, without pod env values or Secret references.
        return {
            "at": utc(),
            "nodes": [describe_node(node) for node in nodes],
            "pods": [describe_pod(pod) for pod in pods],
        }

    def metrics(self):
        node_path = "/apis/metrics.k8s.io/v1beta1/nodes"
        pod_path = f"/apis/metrics.k8s.io/v1beta1/namespaces/{NAMESPACE}/pods"
        return {
            "at": utc(),
            "topology": self.topology(),
            "nodes": self.get("get", "--raw", node_path),
            "pods": self.get("get", "--raw", pod_path),
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
def endpoint(config, cluster, folder):
    """Keep the chosen Aggregator reachable while workers are disrupted."""
    if config.url:
        yield
        return

    pod_name = select_surviving_aggregator(cluster.topology())
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


class EvidenceAPI:
    """Give each probe thread its own HTTP client and serialize evidence writes."""

    def __init__(self, config, folder):
        self.config = config
        self.folder = folder
        self.lock = threading.Lock()
        self.local = threading.local()

    def request(self, path, token, params=None, body=None):
        if not hasattr(self.local, "api"):
            self.local.api = API(
                self.config.url,
                self.config.env_file,
                self.config.request_timeout,
            )

        started = time.monotonic()
        entry = {
            "at": utc(),
            "path": path,
            "method": "POST" if body is not None else "GET",
            "params": params,
        }
        try:
            result = self.local.api.request(path, token, params, body)
            entry.update(ok=True, response=result)
            return result
        except Exception as error:
            entry.update(ok=False, error_type=type(error).__name__)
            raise
        finally:
            entry["seconds"] = time.monotonic() - started
            with self.lock, (self.folder / "http.jsonl").open("a") as stream:
                stream.write(json.dumps(entry, ensure_ascii=False, allow_nan=False) + "\n")


MANUAL_EVIDENCE = [
    "Official assistant dataset/reference provenance and Tester Agent exam logs",
    "VM/native kernels and physical host/topology evidence (container nodes do not qualify)",
    "Browser dashboard: five categories, date filter, severity, area, and partial/stale UI during failure",
    "Per-node disk/write IO and load interpretation from Grafana during import and regional surge",
    "Report: placement, alternatives, consistency guarantees, and contribution explanation",
]


class Run:
    def __init__(self, config, folder):
        self.config = config
        self.folder = folder
        self.cluster = Cluster(config)
        self.api = EvidenceAPI(config, folder)
        self.run_id = folder.name

        # Checkpoints establish what must still exist after the next disruption.
        self.previous_events = []
        self.catalog_identities = set()
        self.acknowledged_writes = set()
        self.surge_refs = set()
        self.storage_plans = {}
        self.development_data = {}
        self.report = {
            "problem": "M2 P1",
            "started_at": utc(),
            "mode": "guided",
            "automated_checks": {},
            "manual_evidence_required": list(MANUAL_EVIDENCE),
            "full_demo_certified": False,
        }

    def save(self, name, data):
        text = json.dumps(data, indent=2, ensure_ascii=False, allow_nan=False)
        (self.folder / name).write_text(text + "\n")

    def stage(self, name, operation, *operation_args, **kwargs):
        """Record one named check, including its result if it fails."""
        output("RUN", name)
        try:
            result = operation(*operation_args, **kwargs)
            self.report["automated_checks"][name] = {"status": "pass", "result": result}
            output("PASS", name)
            print_result(result)
            return result
        except (Exception, KeyboardInterrupt) as error:
            message = str(error) if isinstance(error, (ValueError, RuntimeError)) else type(error).__name__
            status = "interrupted" if isinstance(error, KeyboardInterrupt) else "fail"
            self.report["automated_checks"][name] = {"status": status, "error": message}
            raise
        finally:
            self.save("result.json", self.report)


def verify_credentials(config):
    credentials = API(config.url or "http://127.0.0.1", config.env_file).credentials
    require(
        all(credentials.get(key) for key in ("AGGREGATOR_TOKEN", "AUDIT_TOKEN")),
        "AGGREGATOR_TOKEN and AUDIT_TOKEN are required",
    )
    require(
        credentials["AGGREGATOR_TOKEN"] != credentials["AUDIT_TOKEN"],
        "audit token must be distinct from application token",
    )


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


def verify_environment(run):
    from group2_recovery import read_reference

    verify_credentials(run.config)
    topology = run.cluster.topology()
    run.save("initial-topology.json", topology)
    nodes = topology["nodes"]
    require(
        len(nodes) >= 3 and all(node["ready"] for node in nodes),
        "need at least three Ready nodes for this Citus topology",
    )
    records = catalog.read_catalog(run.config.catalog)
    run.catalog_identities = {("BMKG", record["event_id"]) for record in records}
    if run.config.analytics_end is None:
        latest = max(analytics.timestamp(record["occurred_at"]) for record in records)
        end = latest.replace(hour=0, minute=0, second=0, microsecond=0) + dt.timedelta(days=1)
        run.config.analytics_end = end.isoformat()
    run.report["analytics_end"] = run.config.analytics_end
    reference = read_reference(run.config.reference)
    origin = reference.get("dataset_origin", "external_unverified")
    companion_files = reference.get("companion_files", {})
    if companion_files:
        require(origin == "development_mock" and reference.get("official") is False,
                "companion fixtures are only supported for the development mock reference")
        require(set(companion_files) == {"tsunami_warnings", "volcanic_reports"},
                "development reference must name warning and volcanic fixtures")
        for kind, filename in companion_files.items():
            require(isinstance(filename, str) and Path(filename).name == filename
                    and filename.endswith(".ndjson"), "companion fixture must be a local NDJSON filename")
            path = run.config.reference.parent / filename
            rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
            require(bool(rows) and all(isinstance(row, dict) for row in rows), "empty/invalid companion fixture")
            run.development_data[kind] = rows
        expected_refs = {record["event_id"] for record in records}
        warnings = run.development_data["tsunami_warnings"]
        reports = run.development_data["volcanic_reports"]
        require(all(w["related_event_id"] in expected_refs for w in warnings), "warning is outside the catalog")
        require(len({w["warning_id"] for w in warnings}) == len(warnings), "duplicate warning fixture ID")
        require(len({r["report_id"] for r in reports}) == len(reports), "duplicate volcanic fixture ID")
        require(len(warnings) == reference["tsunami_warning_count"]
                and len(reports) == reference["volcanic_report_count"], "companion counts disagree with reference")
        run.catalog_identities.update(("PVMBG", row["report_id"]) for row in reports)
        run.report["development_fixtures"] = {kind: len(rows) for kind, rows in run.development_data.items()}

    run.report["dataset_origin"] = origin
    run.report["reference_scope"] = reference.get("scope", "source")
    print(f"Dataset: {origin}; reference scope: {run.report['reference_scope']}", flush=True)
    if origin == "development_mock":
        print("Using our development dataset. Official assistant-dataset proof is still pending.", flush=True)
    workers = {node["name"] for node in nodes if not node["control_plane"]}
    require(
        run.config.worker != run.config.second_worker
        and {run.config.worker, run.config.second_worker} <= workers,
        "configure two distinct existing non-control-plane workers in test-problem-1.py",
    )
    if run.config.automatic_worker_actions:
        for worker in (run.config.worker, run.config.second_worker):
            require(worker_action_driver(run.config, worker) == "vagrant-libvirt",
                    "automatic store erasure requires the local Vagrant VM lab")
    run.report["automatic_worker_actions"] = run.config.automatic_worker_actions
    run.report["disk_erasure_verified"] = False
    print("\nM2 P1 — press Enter to continue between test steps.", flush=True)


def print_monitor(summary):
    output("RESULT", "Requests",
           f"Reads: {summary['read_successes']} successful ({summary['partial_responses']} explicitly partial)",
           f"Slowest read: {summary['max_read_seconds']:.3f}s",
           f"Writes: {summary['writes_acknowledged']} acknowledged; {summary['writes_rejected_or_unknown']} rejected/unknown",
           f"Read/metrics errors: {len(summary['errors'])}")


def print_result(result):
    """Keep useful measurements visible; detailed requests stay in evidence files."""
    if not isinstance(result, dict):
        return
    if "records" in result:
        output("RESULT", "Catalog", f"Records: {result['records']}",
               f"Already stored: {result['already_present']}; changed: {result['changed_records']}",
               f"Volcanic reports changed: {result.get('changed_volcanic_reports', 0)}")
    if "events" in result:
        output("RESULT", "Audit", f"Events: {result['events']}", "Previous event identities preserved")
    if result.get("development_fixtures"):
        output("RESULT", "Development fixtures", *(
            f"{kind.replace('_', ' ')}: {count}"
            for kind, count in result["development_fixtures"].items()))
    reference = result.get("reference")
    if reference:
        output("RESULT", "Reference", f"Events matched: {reference['count']}",
               f"SHA-256: {reference['checksum']}")
    reads = result.get("global_reads")
    if reads:
        output("RESULT", "Latest 50 + pagination", "Correct global order",
               f"Stable events compared: {reads['stable_events_compared']}",
               f"Live additions/updates: {reads['live_records_added_or_updated']}",
               f"50-event cursor pages checked: {reads['latest_50_pages_checked']}")
    for label, placement in (("Placement", result.get("placement", result)),
                             ("Regional surge", result.get("surge_distribution"))):
        if not placement or "copies_per_node" not in placement:
            continue
        rows = ["Node | primary events | stored copies"]
        rows.extend(f"{node} | {placement['primary_events_per_node'].get(node, 0)} | {count}"
                    for node, count in sorted(placement["copies_per_node"].items()))
        output("RESULT", label, *rows)
    chart = result.get("analytics", result if "selected_events" in result else None)
    if chart:
        output("RESULT", "Analytics", f"Events matched against Audit API: {chart['selected_events']}",
               f"Time buckets: {chart['buckets']}; areas: {chart['areas']}",
               "Types: " + ", ".join(f"{kind}={count}" for kind, count in chart["by_type"].items()),
               "Severity: " + ", ".join(f"{level}={count}" for level, count in chart["by_severity"].items()))
    storage = result.get("storage_loss")
    if storage:
        status = "erased and verified empty" if storage["operation"] == "erase" else "rebuilt PostgreSQL data"
        output("RESULT", "Storage", f"Worker: {storage['worker']}",
               f"Volumes: {len(storage['volumes'])}; {status}")
    if "acknowledged_writes_preserved" in result:
        output("RESULT", "Durability",
               f"All {result['acknowledged_writes_preserved']} acknowledged test writes are present")


def storage_plan(run, worker):
    """Resolve only this worker's bound AAT store PVCs, with healthy peers."""
    require(worker_action_driver(run.config, worker) == "vagrant-libvirt",
            "automatic storage erasure requires the local Vagrant VM lab")
    pods = run.cluster.get("get", "pods", "-n", NAMESPACE, "-l", "role=store", "-o", "json")["items"]
    nodes = run.cluster.topology()["nodes"]
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
        claim = run.cluster.get("get", "pvc", claims[0], "-n", NAMESPACE, "-o", "json")
        pv = run.cluster.get("get", "pv", claim["spec"]["volumeName"], "-o", "json")
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


# Executed inside the selected lab VM only. Validate every path before deleting
# any contents; retain PVC roots/ownership so Patroni can bootstrap empty data.
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
