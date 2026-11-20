"""P2 cluster access, evidence, polling observations, and cleanup."""
from collections import Counter, defaultdict
from contextlib import contextmanager, nullcontext
import datetime as dt
import hashlib
import json
import math
import os
import re
from pathlib import Path
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from run_progress import Progress

ROOT = Path(__file__).resolve().parents[3]
INFRA = ROOT.parents[1] / "infrastructure"
NAMESPACE = "sistem-informasi-kebencanaan-bnpb"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def read_credentials(env_file):
    credentials = {}
    if env_file:
        for line in Path(env_file).read_text().splitlines():
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            key, separator, value = line.partition("=")
            if not separator or not key.isidentifier():
                raise ValueError("invalid environment file")
            credentials[key] = value
    credentials.update(os.environ)
    return credentials


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def timestamp(value):
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def output(kind, message):
    print(f"[{kind}] {message}", flush=True)


def safe_error(text, credentials):
    for key, value in credentials.items():
        if value and any(word in key.upper() for word in ("TOKEN", "SECRET", "PASSWORD", "KEY")):
            text = text.replace(value, "[REDACTED]")
    text = re.sub(r"(?i)bearer\s+[^\s\"']+", "Bearer [REDACTED]", text)
    text = re.sub(r"([a-z][a-z0-9+.-]*://)[^\s/@]+:[^\s/@]+@", r"\1[REDACTED]@", text)
    return text.strip()[:1500]


def previous_rolling_image(current, running_digests=()):
    """Use a published GitOps version; a missing candidate is a preflight failure."""
    repo = ROOT.parents[2]
    pin = "code/infrastructure/argo-apps/dev/aat/aggregator/image.yaml"
    history = subprocess.run(["git", "log", "-20", "--format=%H", "--", pin],
                             cwd=repo, capture_output=True, text=True, timeout=15)
    require(history.returncode == 0, "cannot read image history; set CONFIG.rolling_image")
    for revision in history.stdout.splitlines():
        result = subprocess.run(["git", "show", revision + ":" + pin], cwd=repo,
                                capture_output=True, text=True, timeout=15)
        repository = re.search(r"^  repository: ([a-zA-Z0-9./:_-]+)$", result.stdout, re.M)
        digest = re.search(r"^  digest: (sha256:[a-f0-9]{64})$", result.stdout, re.M)
        if repository and digest:
            image = repository[1] + "@" + digest[1]
            if image != current and digest[1] not in running_digests:
                return image
    raise ValueError("no different published image in Git history; set CONFIG.rolling_image")


def ready(resource):
    return any(c["type"] == "Ready" and c["status"] == "True"
               for c in resource.get("status", {}).get("conditions", []))


def polling_evidence(records, start, end, poll_interval):
    callers, upstream = {}, []
    successes = Counter()
    for item in records:
        log = item["log"]
        if log.get("msg") == "outbound_request" and log.get("source") in ("BMKG", "PVMBG"):
            key = (log["source"], log.get("path"), log.get("correlation_id"))
            if log.get("status") == 200:
                callers[key] = item
        if not item.get("initial") and start + 2 <= item["observed"] <= end - 2:
            if log.get("msg") == "poll succeeded":
                successes[log.get("source")] += 1
            if item["role"] in ("bmkg", "pvmbg") and log.get("msg") == "request" and log.get("path") in (
                    "/seismic-events", "/tsunami-warnings", "/volcanic-reports"):
                upstream.append(item)
    counts, seen, epochs, intervals = Counter(), set(), {}, defaultdict(list)
    for item in upstream:
        log = item["log"]
        source = item["role"].upper()
        key = (source, log["path"], log.get("correlation_id"))
        require(key not in seen, f"duplicate upstream request correlation: {source} {log['path']}")
        seen.add(key)
        require(log.get("status") == 200, f"{source} upstream returned an error")
        caller = callers.get(key)
        require(caller is not None, f"{source}: upstream request lacks matching Aggregator evidence")
        request = caller["log"]
        owner, epoch = request.get("poll_owner"), request.get("poll_epoch")
        require(owner and str(epoch).isdigit(), "deploy images with X-Poll-Owner/epoch request logging")
        identity = (source, int(epoch))
        authority = (owner, caller["pod_uid"])
        require(identity not in epochs or epochs[identity] == authority, "one polling epoch has multiple owners")
        epochs[identity] = authority
        counts[(source, log["path"])] += 1
        end_time = timestamp(log["time"])
        intervals[(source, log["path"], item["pod_uid"])].append(
            (end_time - log["latency_ms"] / 1000, end_time, owner))
    for samples in intervals.values():
        samples.sort()
        for i, first in enumerate(samples):
            for second in samples[i + 1:]:
                if second[0] >= first[1]:
                    break
                require(first[2] == second[2], "upstream observed overlapping requests from different owners")
    for source, paths in {"BMKG": ("/seismic-events", "/tsunami-warnings"), "PVMBG": ("/volcanic-reports",)}.items():
        require(successes[source] > 0, f"no successful {source} poll in observation window")
        for path in paths:
            require(counts[(source, path)] > 0, f"no upstream evidence for {source} {path}")
            bound = math.ceil((end - start) / poll_interval) + 3
            require(counts[(source, path)] <= bound, f"{source} request rate suggests multiplied pollers")
    return {"upstream_requests": {f"{s}{p}": n for (s, p), n in counts.items()},
            "epochs": [{"source": s, "epoch": e, "owner": o, "pod_uid": p} for (s, e), (o, p) in sorted(epochs.items())],
            "poll_successes": dict(successes), "scope": "observed request/commit logs; not a linearizability proof"}


class Logs:
    def __init__(self, run):
        self.run = run
        self.records, self.errors, self.streams, self.threads = [], [], {}, []
        self.lock, self.stop = threading.Lock(), threading.Event()
        self.traffic = Counter()
        self.seen = set()
        self.pod_states = {}

    def start(self):
        self.watch = threading.Thread(target=self.discover, daemon=True)
        self.watch.start()

    def record_error(self, message):
        message = safe_error(message, self.run.credentials)
        with self.lock:
            self.errors.append(message)
            self.run.save("log-errors.json", list(self.errors))

    def attachable(self, pod, previous):
        if pod["metadata"].get("deletionTimestamp"):
            return False
        if previous is None and not ready(pod):
            return False
        if previous is not None:
            # The pod may start terminating after the discovery snapshot.
            current = next((p for p in self.run.pods()
                            if p["metadata"]["uid"] == pod["metadata"]["uid"]), None)
            if current is None or current["metadata"].get("deletionTimestamp"):
                return False
            self.record_error(f"log stream interrupted for {pod['metadata']['name']} (exit {previous.returncode})")
        return True

    def discover(self):
        while not self.stop.is_set():
            try:
                for pod in self.run.pods():
                    role = pod["metadata"].get("labels", {}).get("role")
                    name = pod["metadata"]["name"]
                    role = "bmkg" if name.startswith("bmkg-") else "pvmbg" if name.startswith("pvmbg-") else role
                    uid = pod["metadata"]["uid"]
                    running = any(s.get("state", {}).get("running") for s in pod.get("status", {}).get("containerStatuses", []))
                    old = self.streams.get(uid)
                    if role in ("aggregator", "bmkg", "pvmbg") and running and (old is None or old.poll() is not None):
                        if not self.attachable(pod, old):
                            continue
                        self.pod_states[uid] = {"pod": name, "attached_at": utc(), "containers": [
                            {"name": s["name"], "restart_count": s.get("restartCount", 0),
                             "last_termination": {k: v for k, v in s.get("lastState", {}).get("terminated", {}).items()
                                                  if k in ("exitCode", "reason", "startedAt", "finishedAt")}}
                            for s in pod.get("status", {}).get("containerStatuses", [])]}
                        self.run.save("log-pods.json", self.pod_states)
                        process = subprocess.Popen(self.run.command("logs", "-f", name, "-n", NAMESPACE, "--tail=1000", "--request-timeout=0"),
                                                   stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
                        self.streams[uid] = process
                        thread = threading.Thread(target=self.read, args=(process, pod, role), daemon=True)
                        self.threads.append(thread)
                        thread.start()
            except Exception as error:
                self.record_error(f"pod discovery: {type(error).__name__}: {error}")
            self.stop.wait(2)

    def read(self, process, pod, role):
        # The initial tail seeds ownership; only live observations count as test traffic.
        attached = time.monotonic()
        for line in process.stdout:
            try:
                log = json.loads(line)
            except (ValueError, TypeError):
                continue
            if log.get("msg") == "request" and str(log.get("correlation_id", "")).startswith(self.run.run_id):
                with self.lock:
                    self.traffic[pod["metadata"]["uid"]] += 1
                continue
            if log.get("msg") not in ("request", "outbound_request", "poll succeeded", "poll ownership acquired", "poll ownership lost"):
                continue
            if role == "aggregator" and log.get("msg") == "request":
                continue
            fingerprint = (pod["metadata"]["uid"], hashlib.sha256(line.encode()).hexdigest())
            log = {k: v for k, v in log.items() if k in ("time", "msg", "source", "path", "status", "latency_ms",
                   "correlation_id", "poll_owner", "poll_epoch", "epoch", "owner")}
            item = {"observed": time.monotonic(), "at": utc(), "initial": time.monotonic() - attached < 1,
                    "pod": pod["metadata"]["name"], "pod_uid": pod["metadata"]["uid"],
                    "node": pod["spec"].get("nodeName"), "role": role, "log": log}
            with self.lock:
                if fingerprint in self.seen:
                    continue
                self.seen.add(fingerprint)
                self.records.append(item)
                with (self.run.folder / "polling.jsonl").open("a") as stream:
                    stream.write(json.dumps(item) + "\n")

    def snapshot(self):
        with self.lock:
            return list(self.records)

    def close(self):
        self.stop.set()
        self.watch.join(timeout=35)
        for process in self.streams.values():
            process.terminate()
        for process in self.streams.values():
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        for thread in self.threads:
            thread.join(timeout=3)


class Run:
    def __init__(self, config, folder, run_id):
        self.config, self.folder, self.run_id = config, folder, run_id
        self.host = run_id + ".localhost"
        self.credentials = {}
        self.logs = Logs(self)
        self.cleanup = []
        self.previous = set()
        self.progress = None
        self.report = {"problem": "M2 P2", "started_at": utc(), "checks": {},
                       "route_name": run_id, "worker": config.worker, "source": config.source,
                       "full_demo_certified": False, "selected_groups": list(config.groups),
                       "manual_evidence_required": ["Official Tester Agent logs", "Clock authority and stale-write fencing",
                                                    "Load generator capacity and resource comparability", "Report and recorded demo"]}

    def command(self, *args):
        return ["kubectl", "--kubeconfig", str(self.config.kubeconfig), "--request-timeout=10s", *args]

    def kube(self, *args, body=None, json_output=True):
        result = subprocess.run(self.command(*args), input=None if body is None else json.dumps(body),
                                capture_output=True, text=True, timeout=30)
        if result.returncode != 0:
            detail = safe_error(result.stderr, self.credentials)
            with (self.folder / "kubectl-errors.jsonl").open("a") as stream:
                stream.write(json.dumps({"at": utc(), "operation": list(args[:3]),
                                         "exit_code": result.returncode, "stderr": detail}) + "\n")
            raise ValueError("kubectl failed: " + " ".join(args[:3]) + (" — " + detail if detail else ""))
        return json.loads(result.stdout) if json_output else result.stdout

    def pods(self):
        return self.kube("get", "pods", "-n", NAMESPACE, "-o", "json")["items"]

    def save(self, name, value):
        (self.folder / name).write_text(json.dumps(value, indent=2, allow_nan=False) + "\n")

    def stage(self, name, operation):
        output("RUN", name)
        try:
            with Progress(self.folder, name, clean=lambda text: safe_error(text, self.credentials),
                          writer=lambda text: output("WAIT", text)) as progress:
                self.progress = progress
                try:
                    result = operation()
                finally:
                    self.progress = None
            self.report["checks"][name] = {"status": "pass", "result": result}
            output("PASS", name)
            return result
        except BaseException as error:
            self.report["checks"][name] = {"status": "fail", "error_type": type(error).__name__}
            raise
        finally:
            self.save("result.json", self.report)

    def skip(self, name, reason):
        self.report["checks"][name] = {"status": "skip", "reason": reason}
        output("SKIP", f"{name}: {reason}")

    def prompt(self, message):
        with self.phase("waiting for Enter", quiet=True):
            input(f"\n{message}\nPress Enter to continue: ")

    def phase(self, description, timeout=None, quiet=False, announce=True):
        progress = getattr(self, "progress", None)
        return progress.phase(description, timeout, quiet, announce) if progress else nullcontext()

    def status(self, detail):
        if getattr(self, "progress", None):
            self.progress.detail(detail)

    def pause(self, seconds, description):
        with self.phase(description, max(0, seconds)):
            time.sleep(max(0, seconds))

    def request(self, path, token="AGGREGATOR_TOKEN", params=None):
        url = self.config.url.rstrip("/") + path
        if params:
            url += "?" + urllib.parse.urlencode(params)
        headers = {"Authorization": "Bearer " + self.credentials[token], "Host": self.host, "Connection": "close",
                   "X-Correlation-ID": self.run_id + "-" + os.urandom(8).hex()}
        request = urllib.request.Request(url, headers=headers)
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        try:
            with opener.open(request, timeout=self.config.request_timeout) as response:
                return json.loads(response.read(8 << 20))
        except urllib.error.HTTPError as error:
            raise ValueError(f"{path}: HTTP {error.code}") from None

    def wait(self, operation, timeout=None, description="required state"):
        timeout = timeout or self.config.recovery_seconds
        deadline = time.monotonic() + timeout
        with self.phase(description, timeout):
            while True:
                result = operation()
                if result:
                    return result
                require(time.monotonic() < deadline, "timed out waiting for " + description)
                time.sleep(1)

    def replicas(self, count, timeout=None):
        self.kube("scale", "deployment/aggregator", "-n", NAMESPACE, f"--replicas={count}", json_output=False)
        def settled():
            deployment = self.kube("get", "deployment", "aggregator", "-n", NAMESPACE, "-o", "json")
            pods = [p for p in self.pods() if p["metadata"].get("labels", {}).get("role") == "aggregator"
                    and not p["metadata"].get("deletionTimestamp")]
            status = deployment.get("status", {})
            self.status(f"target={count}; updated={status.get('updatedReplicas', 0)}; "
                        f"Ready={sum(ready(p) for p in pods)}/{len(pods)}; "
                        + ", ".join(p["metadata"]["name"] + ":" + p.get("status", {}).get("phase", "Unknown")
                                    for p in pods if not ready(p)))
            return (status.get("observedGeneration", 0) >= deployment["metadata"]["generation"]
                    and status.get("updatedReplicas", 0) == count and len(pods) == count
                    and all(ready(p) for p in pods))
        self.wait(settled, timeout=timeout, description=f"Aggregator rollout to {count} Ready replicas")

    def configure_cluster(self, nodes):
        # task up -- --novm records its mode; API metadata also covers copied kubeconfigs.
        mode = INFRA / ".state/mode"
        local_config = self.config.kubeconfig.resolve() == (INFRA / ".state/kubeconfig").resolve()
        self.container_nodes = (local_config and mode.is_file() and mode.read_text().strip() == "k3d") or any(
            n["status"]["nodeInfo"].get("osImage", "").startswith("K3s ")
            or n["metadata"]["name"].startswith("k3d-") for n in nodes)
        self.report["container_nodes_development_only"] = self.container_nodes
        self.report["cluster_mode"] = "k3d" if self.container_nodes else "vm/native"

        if self.config.url is None:
            address = "127.0.0.1"
            if not self.container_nodes:
                control = next((n for n in nodes if n["metadata"]["name"] == "aat-control"
                                or "node-role.kubernetes.io/control-plane" in n["metadata"].get("labels", {})), None)
                addresses = control.get("status", {}).get("addresses", []) if control else []
                address = next((a["address"] for a in addresses if a["type"] == "InternalIP"), None)
                require(address, "cannot determine control-node InternalIP; set CONFIG.url to the reachable Traefik origin")
            host = f"[{address}]" if ":" in address else address
            self.config.url = f"http://{host}:30088"

        self.report["gateway_url"] = self.config.url
        output("INFO", f"Cluster: {self.report['cluster_mode']}; Traefik: {self.config.url}")

    def wait_for_route(self):
        last_issue = "no response yet"

        def routed():
            nonlocal last_issue
            try:
                page = self.request("/internal/hazards", params={"limit": 1})
                if (isinstance(page, dict) and isinstance(page.get("items"), list)
                        and page.get("partial") is False and page.get("unavailable_units") == []):
                    return True
                last_issue = "HazardEvent response is incomplete or invalid"
            except (ValueError, OSError) as error:
                last_issue = safe_error(str(error), self.credentials)
            self.status(last_issue)
            return False

        try:
            # Route propagation is short; a wrong address should not cost the full recovery budget.
            self.wait(routed, timeout=min(30, self.config.recovery_seconds),
                      description=f"Traefik route {self.host} via {self.config.url}")
        except ValueError as error:
            raise ValueError(f"{error}; last response: {last_issue}. "
                             "Check CONFIG.url, Traefik Gateway and HTTPRoute status.") from None

    def preflight(self):
        self.credentials = read_credentials(self.config.env_file)
        require(all(self.credentials.get(k) for k in ("AGGREGATOR_TOKEN", "AUDIT_TOKEN")), "configure Aggregator and Audit tokens")
        require(self.credentials["AGGREGATOR_TOKEN"] != self.credentials["AUDIT_TOKEN"], "Audit token must be separate")
        nodes = self.kube("get", "nodes", "-o", "json")["items"]
        require(len(nodes) >= 3 and all(ready(n) for n in nodes), "need >=3 Ready nodes")
        self.configure_cluster(nodes)
        hpas = self.kube("get", "hpa", "-n", NAMESPACE, "-o", "json")["items"]
        require(not any(h["spec"]["scaleTargetRef"].get("name") == "aggregator" for h in hpas),
                "disable the Aggregator HPA before manual scale measurements")
        pods = self.pods()
        for source, interval in self.config.event_intervals.items():
            mocks = [p for p in pods if p["metadata"]["name"].startswith(source.lower() + "-") and ready(p)]
            require(len(mocks) == 1, f"need one Ready {source} mock for unambiguous request-clock evidence")
            actual = self.kube("exec", mocks[0]["metadata"]["name"], "-n", NAMESPACE, "--", "printenv", "EVENT_INTERVAL_SECONDS", json_output=False)
            require(int(actual.strip()) == interval, f"CONFIG.event_intervals disagrees with live {source} mock")
        aggregator = next((p for p in pods if p["metadata"].get("labels", {}).get("role") == "aggregator" and ready(p)), None)
        require(aggregator, "no Ready Aggregator")
        actual = self.kube("exec", aggregator["metadata"]["name"], "-n", NAMESPACE, "--", "printenv", "POLL_INTERVAL_SECONDS", json_output=False)
        require(float(actual.strip()) == self.config.poll_interval, "CONFIG.poll_interval disagrees with live Aggregator")
        deployment = self.kube("get", "deployment", "aggregator", "-n", NAMESPACE, "-o", "json")
        self.original_replicas = deployment["spec"].get("replicas", 1)
        self.original_template = deployment["spec"]["template"]
        container = next(c for c in deployment["spec"]["template"]["spec"]["containers"] if c["name"] == "aggregator")
        self.original_image = container["image"]
        if 2 in self.config.groups:
            require(not self.container_nodes, "P2.4/P2.5 require separate-kernel VM nodes; choose groups without 2 only for development")
            unsafe = {p["spec"].get("nodeName") for p in pods
                      if p["metadata"]["name"].startswith(("bmkg-", "pvmbg-", "citus-g0-"))}
            safe = sorted(n["metadata"]["name"] for n in nodes if ready(n)
                          and n["metadata"]["name"] in ("aat-worker-1", "aat-worker-2")
                          and n["metadata"]["name"] not in unsafe)
            if self.config.worker is None:
                require(safe, "need a worker without mocks/coordinator for clock/freeze tests")
                self.config.worker = safe[0]
            require(self.config.worker in safe, "configured clock worker hosts a mock/coordinator or is not Ready")
            self.report["clock_freeze_worker"] = self.config.worker
            self.report["worker"] = self.config.worker
        if 3 in self.config.groups:
            current_ids = {c.get("imageID", "").split("@")[-1] for p in pods
                           if p["metadata"].get("labels", {}).get("role") == "aggregator"
                           for c in p.get("status", {}).get("containerStatuses", [])}
            if not self.config.rolling_image:
                self.config.rolling_image = previous_rolling_image(self.original_image, current_ids)
            require(re.fullmatch(r"[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}", self.config.rolling_image),
                    "rolling_image must be a different immutable image digest")
            require(self.config.rolling_image != self.original_image
                    and self.config.rolling_image.split("@")[-1] not in current_ids,
                    "rolling_image matches the running version")
            self.report["rolling_image"] = self.config.rolling_image

        self.save("deployment-baseline.json", {"image": self.original_image, "replicas": self.original_replicas,
                   "resources": container.get("resources"), "strategy": deployment["spec"].get("strategy"),
                   "topology_spread": deployment["spec"]["template"]["spec"].get("topologySpreadConstraints", []),
                   "nodes": [{"name": n["metadata"]["name"], "kernel": n["status"]["nodeInfo"]["kernelVersion"]} for n in nodes]})
        self.prompt("P2 will delete one Aggregator pod, scale replicas, and optionally roll its image. Enter enables the isolated test run.")
        self.report["argo_annotations_before"] = {}
        self.report["argo_automated_before"] = {}
        for name in ("platform", "dev-apps", "aat-aggregator-dev"):
            app = self.kube("get", "application", name, "-n", "argocd", "-o", "json")
            previous = app["metadata"].get("annotations", {}).get("argocd.argoproj.io/skip-reconcile")
            automated = app.get("spec", {}).get("syncPolicy", {}).get("automated")
            self.report["argo_annotations_before"][name] = previous
            self.report["argo_automated_before"][name] = automated
            self.save("result.json", self.report)
            def restore(name=name, previous=previous, automated=automated):
                self.kube("patch", "application", name, "-n", "argocd", "--type=merge", "-p",
                          json.dumps({"metadata": {"annotations": {"argocd.argoproj.io/skip-reconcile": previous}},
                                      "spec": {"syncPolicy": {"automated": automated}}}), json_output=False)
            self.cleanup.append(restore)
            # Disable self-heal before waiting. Skip-reconcile alone freezes
            # status and does not reliably drain an already queued sync.
            self.kube("patch", "application", name, "-n", "argocd", "--type=merge", "-p",
                      json.dumps({"spec": {"syncPolicy": {"automated": None}}}), json_output=False)
            def sync_stopped(name=name):
                current = self.kube("get", "application", name, "-n", "argocd", "-o", "json")
                phase = current.get("status", {}).get("operationState", {}).get("phase")
                self.status(f"Application={name}; sync={phase or 'idle'}")
                return phase not in ("Running", "Terminating") and not current.get("operation")
            self.wait(sync_stopped, description=f"Argo Application {name}: active sync finished")
            self.kube("annotate", "application", name, "-n", "argocd", "argocd.argoproj.io/skip-reconcile=true", "--overwrite", json_output=False)
        self.cleanup.append(self.restore_deployment)
        self.replicas(3)
        if 3 in self.config.groups:
            self.preload_rolling_image(sorted(n["metadata"]["name"] for n in nodes if ready(n)), deployment)
        route = {"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
                 "metadata": {"name": self.run_id, "namespace": NAMESPACE},
                 "spec": {"parentRefs": [{"name": "local", "namespace": "traefik", "sectionName": "web"}],
                          "hostnames": [self.host], "rules": [{"backendRefs": [{"name": "aggregator", "port": 8083}]}]}}
        self.cleanup.append(lambda: self.kube("delete", "httproute", self.run_id, "-n", NAMESPACE, "--ignore-not-found", json_output=False))
        self.kube("create", "-f", "-", body=route, json_output=False)
        self.wait_for_route()
        self.logs.start()
        self.logs_started = True
        time.sleep(3)
        return {"replicas": 3, "load_balancer": "Traefik -> Aggregator Service", "host": self.host,
                "gateway_url": self.config.url, "cluster_mode": self.report["cluster_mode"],
                "gitops": "disable auto-sync, drain operations, then skip reconciliation; restore in finally"}

    def preload_rolling_image(self, nodes, deployment):
        name = self.run_id + "-image"
        selector = {"app": name}
        manifest = {"apiVersion": "apps/v1", "kind": "DaemonSet",
            "metadata": {"name": name, "namespace": NAMESPACE},
            "spec": {"selector": {"matchLabels": selector}, "template": {
                "metadata": {"labels": dict(selector, role="other")},
                "spec": {"automountServiceAccountToken": False,
                    "tolerations": [{"operator": "Exists"}],
                    "affinity": {"nodeAffinity": {"requiredDuringSchedulingIgnoredDuringExecution": {
                        "nodeSelectorTerms": [{"matchExpressions": [{"key": "kubernetes.io/hostname",
                            "operator": "In", "values": nodes}]}]}}},
                    "imagePullSecrets": deployment["spec"]["template"]["spec"].get("imagePullSecrets", []),
                    "containers": [{"name": "image", "image": self.config.rolling_image,
                        "imagePullPolicy": "IfNotPresent", "command": ["/bin/sh", "-c", "sleep 3600"],
                        "securityContext": {"runAsNonRoot": True, "runAsUser": 10001,
                                            "allowPrivilegeEscalation": False},
                        "resources": {"requests": {"cpu": "1m", "memory": "8Mi"},
                                      "limits": {"cpu": "100m", "memory": "64Mi"}}}]}}}}
        def remove():
            self.kube("delete", "daemonset", name, "-n", NAMESPACE, "--ignore-not-found", json_output=False)
        self.cleanup.append(remove)
        try:
            self.kube("create", "-f", "-", body=manifest, json_output=False)
            def cached():
                pods = self.kube("get", "pods", "-n", NAMESPACE, "-l", "app=" + name, "-o", "json")["items"]
                healthy = [p for p in pods if ready(p)]
                self.status(f"rolling image cached on {len(healthy)}/{len(nodes)} nodes")
                return {p["spec"]["nodeName"] for p in healthy} == set(nodes)
            self.wait(cached, description="preloading mandatory rolling image on every node")
            self.report["rolling_image_preloaded_nodes"] = nodes
        finally:
            remove()
            self.cleanup.pop()

    def deployment_state(self):
        deployment = self.kube("get", "deployment", "aggregator", "-n", NAMESPACE, "-o", "json")
        template = deployment["spec"]["template"]
        pods = [pod for pod in self.pods()
                            if pod["metadata"].get("labels", {}).get("role") == "aggregator"
                            and not pod["metadata"].get("deletionTimestamp") and ready(pod)]
        image_ids = sorted({container["imageID"] for pod in pods
                            for container in pod.get("status", {}).get("containerStatuses", [])
                            if container["name"] == "aggregator"})
        require(image_ids, "no Ready Aggregator image IDs for throughput comparison")
        container = next(c for c in template["spec"]["containers"] if c["name"] == "aggregator")
        read_limits = set()
        for pod in pods:
            # Only these public settings are exported; never dump the pod environment.
            values = self.kube("exec", pod["metadata"]["name"], "-n", NAMESPACE, "-c", "aggregator", "--",
                               "sh", "-c", 'printf "%s\\n%s\\n" "${READ_RPS_LIMIT:-0}" "${READ_RPS_BURST:-5}"',
                               json_output=False).splitlines()
            require(len(values) == 2, "could not read Aggregator admission settings")
            read_limits.add(tuple(map(int, values)))
        require(len(read_limits) == 1, "Ready Aggregators have different read rate limits")
        rps, burst = next(iter(read_limits))
        return {"image": container["image"], "image_ids": image_ids,
                "nodes": sorted({pod["spec"]["nodeName"] for pod in pods}),
                "read_admission": {"rps_per_replica": rps, "burst_per_replica": burst},
                "template_sha256": hashlib.sha256(json.dumps(template, sort_keys=True).encode()).hexdigest()}

    def restore_deployment(self):
        self.kube("patch", "deployment", "aggregator", "-n", NAMESPACE, "--type=json", "-p",
                  json.dumps([{"op": "replace", "path": "/spec/template", "value": self.original_template}]), json_output=False)
        self.replicas(self.original_replicas)

    def audit(self):
        with self.phase("exporting Audit events and checking identities"):
            return self._audit()

    def _audit(self):
        cursor, seen, identities, items, versions, hazard_keys = None, set(), {}, [], set(), {}
        for _ in range(10000):
            params = {"limit": 1000}
            if cursor:
                params["cursor"] = cursor
            page = self.request("/internal/audit/events", "AUDIT_TOKEN", params)
            self.status(f"page={_ + 1}; events read={len(items) + len(page.get('items', []))}")
            require(page.get("partial") is False and page.get("unavailable_units") == [], "Audit export is incomplete")
            for event in page["items"]:
                key = (event["source"], event["source_ref_id"])
                identity = event["hazard_id"]
                require(identity not in hazard_keys or hazard_keys[identity] == key, "one hazard ID has multiple source identities")
                hazard_keys[identity] = key
                version = (identity, event["ingested_at"])
                require(version not in versions, "Audit repeated an identical event version")
                versions.add(version)
                require(key not in identities or identities[key] == identity, "logical event has two hazard IDs")
                identities[key] = identity
                items.append(event)
            cursor = page.get("next_cursor")
            if cursor is None:
                require(self.previous <= set(identities), "previously observed events disappeared")
                self.previous.update(identities)
                return items
            require(cursor not in seen, "Audit cursor loop")
            seen.add(cursor)
        raise ValueError("Audit page limit exceeded")

    def checkpoint(self, name, start_wall=None, end_wall=None):
        def reconcile():
            try:
                events = self.audit()
                expected = Counter()
                if start_wall is not None:
                    found = {(e["source"], e["source_ref_id"]) for e in events}
                    for source, interval in self.config.event_intervals.items():
                        first = math.ceil((start_wall + interval) / interval) * interval
                        for second in range(first, math.floor(end_wall - interval) + 1, interval):
                            expected[source] += 1
                            require((source, source.lower() + "-" + str(second * 1_000_000_000)) in found,
                                    f"{source}: gap in deterministic mock events")
                return {"logical_events": len({(e['source'], e['source_ref_id']) for e in events}),
                        "previous_identities_preserved": True, "mock_window": [start_wall, end_wall],
                        "expected_mock_events_checked": dict(expected)}
            except ValueError as error:
                self.status("last reconciliation issue: " + str(error))
                return False
        result = self.wait(reconcile, description="Audit reconciliation: " + name)
        self.save(name + "-audit.json", result)
        return result

    def observe(self):
        start, wall = time.monotonic(), time.time()
        self.audit()
        self.pause(self.config.observation_seconds, "observing upstream polling and ownership")
        end = time.monotonic()
        time.sleep(2)
        require(not self.logs.errors, "log collection has errors; evidence is incomplete: " + "; ".join(self.logs.errors[:3]))
        result = polling_evidence(self.logs.snapshot(), start, end, self.config.poll_interval)
        result["audit"] = self.checkpoint("observation-" + os.urandom(3).hex(), wall, time.time())
        return result

    def owner(self, source):
        candidates = [r for r in self.logs.snapshot() if r["log"].get("source") == source
                      and r["log"].get("msg") == "outbound_request" and r["log"].get("poll_epoch")]
        require(candidates, f"no ownership evidence for {source}")
        return max(candidates, key=lambda r: (int(r["log"]["poll_epoch"]), r["observed"]))

    def __enter__(self):
        self.logs_started = False
        return self

    def __exit__(self, *_):
        with Progress(self.folder, "cleanup", clean=lambda text: safe_error(text, self.credentials),
                      writer=lambda text: output("WAIT", text)) as progress:
            self.progress = progress
            try:
                self.restore_run()
            finally:
                self.progress = None

    def restore_run(self):
        errors = []
        for index, operation in enumerate(reversed(self.cleanup), 1):
            try:
                with self.phase(f"restoring cluster settings {index}/{len(self.cleanup)}"):
                    operation()
            except BaseException as error:
                errors.append(type(error).__name__)
        if self.logs_started:
            with self.phase("closing log collector"):
                self.logs.close()
        self.report["cleanup_errors"] = errors
        if errors:
            raise ValueError("cleanup incomplete: restore deployment, workers, and Argo reconciliation before another run")
