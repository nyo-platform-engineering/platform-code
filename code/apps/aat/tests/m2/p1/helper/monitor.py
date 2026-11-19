"""Background read/write probes, HTTP clients, and request evidence."""
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
from tempfile import TemporaryDirectory
import threading
import time
import urllib.parse
import uuid

from common import output, require, utc
from .cluster import cluster_metrics
from .data import availability, event_key


class TransportError(RuntimeError):
    """The HTTP exchange failed before receiving a valid HTTP response."""


class API:
    def __init__(self, url, env_file=None, timeout=10):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
            raise ValueError("URL must be an http(s) origin without credentials, query, or path")
        self.url = url.rstrip("/")
        self.timeout = timeout
        self.credentials = {}
        if env_file:
            for line in Path(env_file).read_text().splitlines():
                if not line.strip() or line.lstrip().startswith("#"):
                    continue
                key, separator, value = line.partition("=")
                if not separator or not key.isidentifier():
                    raise ValueError("invalid environment file")
                self.credentials[key] = value
        self.credentials.update(os.environ)
        self.curl = shutil.which("curl")
        require(self.curl is not None, "curl is required for HTTP requests with a total deadline")

    def request(self, path, token_key, params=None, body=None, timeout=None):
        timeout = self.timeout if timeout is None else timeout
        if timeout <= 0:
            raise TimeoutError(f"{path}: request deadline exhausted")
        token = self.credentials.get(token_key)
        if not token:
            raise ValueError(f"set {token_key} or provide --env-file")
        url = self.url + path
        if params:
            url += "?" + urllib.parse.urlencode(params)
        data = None if body is None else json.dumps(body, ensure_ascii=False, allow_nan=False).encode()
        if data is not None and len(data) > 2 << 20:
            raise ValueError("batch exceeds 2 MiB; use a smaller --batch-size")
        require("\r" not in token and "\n" not in token, "invalid API token")
        headers = f"Authorization: Bearer {token}\nContent-Type: application/json\n"
        with TemporaryDirectory(prefix="p1-http-") as folder:
            response_file = Path(folder) / "response.json"
            command = [
                self.curl, "--disable", "--silent", "--show-error",
                "--proto", "=http,https", "--max-time", str(timeout),
                "--max-filesize", str(8 << 20), "--header", "@-",
                "--output", str(response_file), "--write-out", "%{http_code}",
            ]
            if data is not None:
                body_file = Path(folder) / "request.json"
                body_file.write_bytes(data)
                command.extend(["--data-binary", "@" + str(body_file)])
            command.extend(["--url", url])
            try:
                response = subprocess.run(command, input=headers, capture_output=True,
                                          text=True, timeout=timeout)
            except subprocess.TimeoutExpired:
                raise TimeoutError(f"{path}: request exceeded {timeout}s deadline") from None
            if response.returncode == 28:
                raise TimeoutError(f"{path}: request exceeded {timeout}s deadline")
            if response.returncode == 63:
                raise ValueError("API response exceeds 8 MiB")
            if response.returncode in (5, 6, 7, 18, 52, 55, 56):
                raise TransportError(f"cannot reach {path} (curl exit {response.returncode})")
            if response.returncode != 0:
                raise RuntimeError(f"cannot reach {path} (curl exit {response.returncode})")
            status = int(response.stdout)
            if not 200 <= status < 300:
                raise RuntimeError(f"{path} returned HTTP {status}")
            if response_file.stat().st_size > 8 << 20:
                raise ValueError("API response exceeds 8 MiB")
            result = json.loads(response_file.read_bytes())
            if not isinstance(result, dict):
                raise ValueError("expected a JSON object response")
            return result



class EvidenceAPI:
    """Give each probe thread its own HTTP client and serialize evidence writes."""

    def __init__(self, config, folder):
        self.config = config
        self.folder = folder
        self.lock = threading.Lock()
        self.local = threading.local()
        # Both endpoints share one read-admission slot in the default lab.
        # Serialize runner reads across foreground checks and background probes.
        self.read_slot = threading.Lock()

    def request(self, path, token, params=None, body=None):
        started = time.monotonic()
        deadline = started + self.config.request_timeout
        entry = {
            "at": utc(), "path": path,
            "method": "POST" if body is not None else "GET", "params": params,
        }
        read_slot_acquired = False
        try:
            if path in ("/internal/hazards", "/internal/analytics"):
                queued_at = time.monotonic()
                read_slot_acquired = self.read_slot.acquire(timeout=max(0, deadline - queued_at))
                entry["read_queue_seconds"] = time.monotonic() - queued_at
                if not read_slot_acquired:
                    raise TimeoutError(f"{path}: runner read queue exceeded the request deadline")
            ensure = getattr(self.config, "ensure_endpoint", None)
            if ensure:
                entry["tunnel_reconnected"] = ensure(deadline - time.monotonic())
            generation = getattr(self.config, "endpoint_generation", None)
            if not hasattr(self.local, "api"):
                self.local.api = API(self.config.url, self.config.env_file, self.config.request_timeout)
            try:
                result = self.local.api.request(path, token, params, body, timeout=deadline - time.monotonic())
            except TransportError as error:
                entry["transport_error"] = str(error)
                # GET is safe to repeat only when kubectl's own tunnel has exited.
                # Never replay writes with an unknown outcome or retry a live server's errors.
                if body is not None or ensure is None or not ensure(
                    deadline - time.monotonic(), failed_generation=generation,
                ):
                    raise
                entry["tunnel_reconnected"] = True
                entry["read_retried"] = True
                result = self.local.api.request(path, token, params, body, timeout=deadline - time.monotonic())
            entry.update(ok=True, response=result)
            return result
        except Exception as error:
            entry.update(ok=False, error_type=type(error).__name__, error=str(error))
            raise
        finally:
            if read_slot_acquired:
                self.read_slot.release()
            entry["seconds"] = time.monotonic() - started
            with self.lock, (self.folder / "http.jsonl").open("a") as stream:
                stream.write(json.dumps(entry, ensure_ascii=False, allow_nan=False) + "\n")


@dataclass
class ReadProbe:
    path: str
    token: str
    params: dict
    kind: str


class Monitor:
    """Probe reads/writes and sample metrics for the duration of a test step."""

    def __init__(self, run, name, writes, target=None, expect_ready=None):
        self.run = run
        self.name = name
        self.writes = writes
        self.target = target
        self.expect_ready = expect_ready
        self.stop = threading.Event()
        self.reconnections_before = len(getattr(run.config, "endpoint_reconnections", []))

        self.errors = []
        self.reads = 0
        self.read_seconds = []
        self.partial = 0
        self.write_ok = 0
        self.write_failed = 0
        self.metric_samples = 0
        self.metric_times = {}
        self.node_load = {}
        self.target_seen = False
        self.target_ready = None

    def __enter__(self):
        self.started = time.monotonic()
        self.last_progress_at = self.started
        self.threads = [
            threading.Thread(target=self.sample_requests),
            threading.Thread(target=self.sample_metrics),
        ]
        for thread in self.threads:
            thread.start()
        return self

    def read_probes(self):
        probes = [
            ReadProbe("/internal/audit/events", "AUDIT_TOKEN", {"limit": 50, "source": "BMKG"}, "audit"),
            ReadProbe("/internal/audit/checksum", "AUDIT_TOKEN", {"source": "BMKG"}, "checksum"),
            ReadProbe("/internal/hazards", "AGGREGATOR_TOKEN", {"order": "recent", "limit": 50}, "recent"),
        ]
        if hasattr(self.run, "analytics_query"):
            probes.append(ReadProbe(
                "/internal/analytics", "AGGREGATOR_TOKEN", self.run.analytics_query, "analytics",
            ))
        return probes

    def probe_read(self, probe):
        started = time.monotonic()
        response = self.run.api.request(probe.path, probe.token, probe.params)
        elapsed = time.monotonic() - started
        require(elapsed <= self.run.config.max_read_seconds, f"{probe.kind}: read exceeded latency bound")
        self.read_seconds.append(elapsed)
        availability(response, probe.kind)

        if self.writes and not response["partial"]:
            self.verify_complete_read(probe.kind, response)
        return response["partial"]

    def verify_complete_read(self, kind, response):
        """A complete response must still contain the data known before the loss."""
        stable_bmkg = [event for event in self.run.previous_events if event["source"] == "BMKG"]
        if kind == "checksum":
            # Live polls can add BMKG events during recovery. The exact controlled
            # catalog checksum is checked after recovery; this probe rejects lost totals.
            require(type(response.get("count")) is int and response["count"] >= len(stable_bmkg),
                    "complete checksum lost known event count")
            require(response.get("algorithm") == "sha256" and isinstance(response.get("checksum"), str)
                    and re.fullmatch("[0-9a-f]{64}", response["checksum"]), "invalid complete checksum")
        elif kind == "audit":
            require(len(response["items"]) == min(50, len(stable_bmkg)), "complete audit page lost known data")
            keys = [event_key(event, "audit") for event in response["items"]]
            require(all(a < b for a, b in zip(keys, keys[1:])), "audit page order violated")
        elif kind == "recent":
            require(len(response["items"]) == 50, "complete latest-50 page lost known data")
            keys = [event_key(event, "recent") for event in response["items"]]
            require(all(a > b for a, b in zip(keys, keys[1:])), "latest-50 global order violated")
        elif kind == "analytics":
            total = response.get("total")
            require(
                type(total) is int and total >= self.run.baseline_analytics["total"],
                "complete analytics lost known events",
            )

    def sample_requests(self):
        with ThreadPoolExecutor(max_workers=4) as pool:
            while not self.stop.is_set():
                started = time.monotonic()
                reads = [pool.submit(self.probe_read, probe) for probe in self.read_probes()]
                write = pool.submit(self.probe_write) if self.writes else None
                for future in reads:
                    try:
                        self.partial += int(future.result())
                        self.reads += 1
                    except Exception as error:
                        message = str(error) or type(error).__name__
                        if message not in self.errors:
                            output("PROBE GAGAL", f"{self.name} — pembacaan", message,
                                   "Kesalahan ini dicatat dan akan menggagalkan tahap; "
                                   "tidak dianggap respons parsial yang sah.")
                        self.errors.append(message)
                if write:
                    write.result()
                if time.monotonic() - self.last_progress_at >= 10:
                    output("PROGRESS", self.name,
                           f"Reads: {self.reads} successful ({self.partial} partial)",
                           f"Writes: {self.write_ok} acknowledged; {self.write_failed} rejected/unknown",
                           f"Metrics samples: {self.metric_samples}",
                           f"Elapsed: {time.monotonic() - self.started:.0f}s",
                           self.action_hint())
                    self.last_progress_at = time.monotonic()
                elapsed = time.monotonic() - started
                self.stop.wait(max(0, self.run.config.sample_interval - elapsed))

    def action_hint(self):
        if not self.target:
            return ""
        state = "unknown" if self.target_ready is None else "Ready" if self.target_ready else "NotReady"
        if self.run.config.automatic_worker_actions:
            action = "automatic worker action / node-state verification in progress"
        elif self.target_ready == self.expect_ready:
            action = "required state observed; press Enter to continue"
        elif self.expect_ready:
            action = "restore the worker with empty storage, then press Enter"
        else:
            action = "stop the worker and erase its storage, then press Enter"
        return f"Worker: {self.target} ({state})\nAction: {action}"

    def probe_write(self):
        reference = f"{self.run.run_id}-continuity-{uuid.uuid4().hex}"
        report = {
            "report_id": reference,
            "volcano_id": "MERAPI",
            "reported_at": utc(),
            "alert_level": "Waspada",
            "ash_column_height_m": 100,
            "eruption_count_24h": 1,
        }
        try:
            result = self.run.api.request(
                "/internal/ingest/pvmbg", "AGGREGATOR_TOKEN",
                body={"volcanic_reports": [report]},
            )
            require(result.get("changed") == 1, "continuity write was not acknowledged as new")
            self.run.acknowledged_writes.add(reference)
            self.write_ok += 1
        except Exception:
            # Unknown outcomes are never counted as acknowledged durable writes.
            self.write_failed += 1

    def inspect_metrics(self, sample):
        require(
            bool(sample["nodes"].get("items")) and bool(sample["pods"].get("items")),
            "metrics API returned no samples",
        )
        nodes = sample["topology"]["nodes"]
        ready_nodes = {node["name"] for node in nodes if node["ready"]}
        measured_nodes = {node["metadata"]["name"] for node in sample["nodes"]["items"]}
        unaffected_nodes = ready_nodes - {self.target}
        require(unaffected_nodes <= measured_nodes, "metrics missing an unaffected Ready node")

        for metric in sample["nodes"]["items"]:
            node_name = metric["metadata"]["name"]
            timestamps = self.metric_times.setdefault(node_name, set())
            timestamps.add(metric["timestamp"])
            if "usage" in metric:
                # Present Kubernetes quantities as millicores and MiB for the demo.
                usage = {}
                for field, scales in (
                    ("cpu", {"n": 0.000001, "u": 0.001, "m": 1}),
                    ("memory", {"Ki": 2**10, "Mi": 2**20, "Gi": 2**30, "Ti": 2**40,
                                "K": 10**3, "M": 10**6, "G": 10**9}),
                ):
                    raw = metric["usage"][field]
                    for suffix, scale in scales.items():
                        if raw.endswith(suffix):
                            usage[field] = float(raw[:-len(suffix)]) * scale
                            break
                    else:
                        usage[field] = float(raw) * (1000 if field == "cpu" else 1)
                samples = self.node_load.setdefault(node_name, [])
                samples.append((usage["cpu"], usage["memory"] / 2**20))

        for node in nodes:
            if node["name"] == self.target:
                self.target_ready = node["ready"]
                if node["ready"] == self.expect_ready:
                    self.target_seen = True
        self.metric_samples += 1

    def sample_metrics(self):
        path = self.run.folder / f"{self.name}-metrics.jsonl"
        while not self.stop.is_set():
            try:
                sample = cluster_metrics(self.run.config)
                self.inspect_metrics(sample)
            except Exception as error:
                message = str(error) or type(error).__name__
                sample = {"at": utc(), "error": message}
                self.errors.append("metrics: " + message)
            with path.open("a") as stream:
                stream.write(json.dumps(sample) + "\n")
            self.stop.wait(self.run.config.metrics_interval)

    def summary(self):
        return {
            "read_successes": self.reads,
            "tunnel_reconnections": len(getattr(self.run.config, "endpoint_reconnections", [])) - self.reconnections_before,
            "max_read_seconds": round(max(self.read_seconds, default=0), 3),
            "partial_responses": self.partial,
            "writes_acknowledged": self.write_ok,
            "writes_rejected_or_unknown": self.write_failed,
            "metric_samples": self.metric_samples,
            "node_load": {
                node: {
                    "samples": len(samples),
                    "cpu_avg_millicores": round(sum(cpu for cpu, _ in samples) / len(samples), 2),
                    "cpu_peak_millicores": round(max(cpu for cpu, _ in samples), 2),
                    "memory_avg_mib": round(sum(memory for _, memory in samples) / len(samples), 2),
                    "memory_peak_mib": round(max(memory for _, memory in samples), 2),
                } for node, samples in self.node_load.items()
            },
            "target_state_observed": self.target_seen,
            "last_observed_target_ready": self.target_ready,
            "distinct_node_metric_timestamps": {
                node: len(times) for node, times in self.metric_times.items()
            },
            "errors": self.errors,
            "seconds": getattr(self, "ended", time.monotonic()) - self.started,
        }

    def verify_window(self):
        if self.errors:
            output("FAIL", f"{self.name} — rincian probe gagal",
                   *(f"{count}x: {message}" for message, count in Counter(self.errors).items()),
                   f"Bukti: {self.run.folder / (self.name + '-monitor.json')}",
                   "HTTP 429 berarti kapasitas baca ditolak server; HTTP 500 dan timeout juga tetap gagal.")
            raise ValueError(f"{self.name}: {len(self.errors)} probe gagal; "
                             f"penyebab pertama: {self.errors[0]}")
        require(self.reads >= 3 and self.metric_samples >= 2, f"{self.name}: insufficient read/metrics samples")
        require(self.metrics_are_fresh(), f"{self.name}: metrics did not refresh")
        require(not self.target or self.target_seen, f"{self.name}: target node state was never observed")
        if self.writes:
            require(self.write_ok > 0, f"{self.name}: no successful concurrent writes")
            if self.expect_ready:
                require(self.write_failed == 0, f"{self.name}: write interruption while rejoining")

    def metrics_are_fresh(self):
        return bool(self.metric_times) and all(
            len(times) >= 2 for node, times in self.metric_times.items()
            if node != self.target
        )

    def __exit__(self, kind, value, traceback):
        try:
            if kind is None:
                remaining = self.run.config.min_window_seconds - (time.monotonic() - self.started)
                if remaining > 0:
                    output("INFO", "Information", f"collecting {remaining:.0f}s more evidence for {self.name}")
                    self.stop.wait(remaining)
                while not self.errors and not self.metrics_are_fresh():
                    output("WAIT", "Waiting", f"{self.name}: waiting for a second distinct node metrics timestamp")
                    self.stop.wait(self.run.config.metrics_interval)
        finally:
            self.stop.set()
            for thread in self.threads:
                thread.join()
            self.ended = time.monotonic()
            self.run.save(f"{self.name}-monitor.json", self.summary())
            summary = self.summary()
            output("RESULT", "Requests",
                   f"Reads: {summary['read_successes']} successful ({summary['partial_responses']} explicitly partial)",
                   f"Slowest read: {summary['max_read_seconds']:.3f}s",
                   f"Writes: {summary['writes_acknowledged']} acknowledged; {summary['writes_rejected_or_unknown']} rejected/unknown",
                   f"Read/metrics errors: {len(summary['errors'])}")
            if summary["tunnel_reconnections"]:
                output("CONNECTION", f"Tunnel diperbaiki {summary['tunnel_reconnections']} kali selama {self.name}",
                       "Gangguan transport dan GET yang diulang tetap tercatat di http.jsonl; "
                       "ini bukan respons parsial dari server.")
            for node, load in summary["node_load"].items():
                output("LOAD", f"Beban node {node} — {self.name}",
                       f"CPU rata-rata/puncak: {load['cpu_avg_millicores']:.2f} / "
                       f"{load['cpu_peak_millicores']:.2f} millicore (1000 = 1 core)",
                       f"Memori rata-rata/puncak: {load['memory_avg_mib']:.2f} / "
                       f"{load['memory_peak_mib']:.2f} MiB; sampel: {load['samples']}",
                       "Ini penggunaan seluruh node; disk/write IO dan penyebab ketimpangan "
                       "tetap perlu dibandingkan dengan Grafana.")
        if kind is None:
            self.verify_window()
