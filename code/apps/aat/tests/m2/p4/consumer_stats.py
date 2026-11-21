"""Consumer statistics from the Aggregators' own logs.

Two log lines are used:
- "satellite batch committed" (every batch): size (events), commit time (commit_ms),
  the arrival of its oldest event to commit (ingest_latency_ms) and the feed offset;
- "satellite feed progress" (every 10 s, from the owner): the feed lag (lag_events).

Time is cut into CONFIG.window_seconds windows. A window is *behind* when every lag
sample around it is at least CONFIG.catch_up_min_lag events, and *current* when every
sample is at most CONFIG.max_lag_events; windows in between (the start and end of a
catch-up) and windows without samples are left out. Batch sizes alone can't tell:
while behind, full 200-event batches alternate with tiny ones whenever the 0.5 s
flush fires between them. A window holding a full batch (and the one after it) is
never current, though: a restart's short catch-up often fits between two samples.

- capacity: events/s in the behind windows (median, with p10 and p90), i.e. the real
  speed of this cluster with reading and committing included;
- commit cost: commit_ms = fixed + per_event * events (least squares), used when the
  logs hold no catch-up (optimistic: it leaves out reading time);
- live rate: events/s in the current windows, average and the busiest
  CONFIG.peak_window_seconds.

The lines come from Loki (CONFIG.loki_lookback_hours back), which keeps them after
pods restart and after the busy pod logs rotate, so a catch-up an hour ago still
counts. Without Loki it falls back to the current pods' logs, which is why this is
read before group 3 deletes the pods.
"""
from contextlib import contextmanager
import collections
import json
import re
import socket
import subprocess
import time
import urllib.parse
import urllib.request

from common import NAMESPACE, json_logs, output, p95, parse_time

BATCH, PROGRESS = "satellite batch committed", "satellite feed progress"
POD = re.compile(r"^\[pod/([^/\]]+)")
LOKI_NAMESPACE, LOKI_SERVICE, LOKI_PORT = "monitoring", "loki", 3100
LOKI_PAGE = 5000  # Loki's default max_entries_limit_per_query


def parse(pod, record, batches, progress):
    if record.get("msg") == BATCH:
        batches.append({"pod": pod, "time": record["time"], "offset": record.get("offset"),
                        "events": record.get("events", 0), "commit_ms": record.get("commit_ms", 0),
                        "ingest_latency_ms": record.get("ingest_latency_ms")})
    elif record.get("msg") == PROGRESS and isinstance(record.get("lag_events"), int):
        progress.append({"pod": pod, "time": record["time"], "lag_events": record["lag_events"]})


def ordered(rows):
    return sorted(rows, key=lambda r: (parse_time(r["time"]), r.get("offset") or 0))


def pod_logs(run):
    text = run.cluster.kubectl("logs", "-l", "app.kubernetes.io/instance=aggregator", "--tail=-1",
                               "--prefix=true", "--max-log-requests=10", timeout=120)
    batches, progress = [], []
    for line in text.splitlines():
        match = POD.match(line)
        for record in json_logs(line, BATCH, PROGRESS):
            parse(match.group(1) if match else "", record, batches, progress)
    return batches, progress


@contextmanager
def loki_tunnel(run):
    """A temporary port-forward to Loki (another namespace than the app's)."""
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    log = (run.folder / "port-forward-loki.log").open("a")
    process = subprocess.Popen(
        ["kubectl", "--kubeconfig", str(run.config.kubeconfig), "-n", LOKI_NAMESPACE, "port-forward",
         f"svc/{LOKI_SERVICE}", f"{port}:{LOKI_PORT}", "--address=127.0.0.1"], stdout=log, stderr=log)
    try:
        deadline = time.monotonic() + 20
        while True:
            if process.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError("port-forward to Loki did not start; see port-forward-loki.log")
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.3):
                    break
            except OSError:
                time.sleep(0.2)
        yield f"http://127.0.0.1:{port}"
    finally:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
        log.close()


def loki_logs(run):
    """Every batch and progress line of the last loki_lookback_hours, paged forward."""
    query = (f'{{k8s_namespace_name="{NAMESPACE}", service_name="aggregator"}} '
             f'|~ "satellite (batch committed|feed progress)"')
    end = time.time_ns()
    start = end - int(run.config.loki_lookback_hours * 3600 * 1e9)
    batches, progress, seen = [], [], set()
    with loki_tunnel(run) as base:
        while start < end:
            url = base + "/loki/api/v1/query_range?" + urllib.parse.urlencode(
                {"query": query, "start": start, "end": end, "limit": LOKI_PAGE, "direction": "forward"})
            with urllib.request.urlopen(url, timeout=120) as response:
                result = json.load(response)["data"]["result"]
            page = [(int(ts), stream["stream"].get("k8s_pod_name", ""), line)
                    for stream in result for ts, line in stream["values"]]
            for ts, pod, line in page:
                for record in json_logs(line, BATCH, PROGRESS):
                    key = (pod, record.get("msg"), record["time"])
                    if key not in seen:  # page boundaries can repeat a line
                        seen.add(key)
                        parse(pod, record, batches, progress)
            if len(page) < LOKI_PAGE:
                break
            newest = max(ts for ts, _, _ in page)  # next page from the newest line seen
            start = newest if newest > start else start + 1
    return batches, progress


def collect(run):
    """(batches, progress, source): Loki first, the current pods' logs if Loki can't be read."""
    if run.config.use_loki:
        try:
            batches, progress = loki_logs(run)
            return ordered(batches), ordered(progress), f"Loki, last {run.config.loki_lookback_hours:g} h"
        except (OSError, RuntimeError, ValueError, KeyError) as error:
            output("WARN", "Loki not readable; using the current pods' logs", str(error)[:200])
    batches, progress = pod_logs(run)
    return ordered(batches), ordered(progress), "current pod logs"


def fit(rows):
    """commit_ms = fixed + per_event * events, or None without a spread of sizes."""
    xs, ys = [r["events"] for r in rows], [r["commit_ms"] for r in rows]
    if len(rows) < 5 or max(xs) - min(xs) < 20:
        return None
    mx, my = sum(xs) / len(xs), sum(ys) / len(ys)
    sxx = sum((x - mx) ** 2 for x in xs)
    per_event = max(0.0, sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / sxx)
    return {"fixed_ms": round(max(0.0, my - per_event * mx), 2), "per_event_ms": round(per_event, 3)}


def epoch(row):
    return parse_time(row["time"]).timestamp()


def classify(progress, config):
    """{window index: "behind" | "current"} from the lag samples around each window."""
    width = config.window_seconds
    samples = collections.defaultdict(list)
    for p in progress:
        # A sample describes the windows it falls in and next to (samples are 10 s apart).
        index = int(epoch(p) // width)
        for i in (index - 1, index, index + 1):
            samples[i].append(p["lag_events"])
    kinds = {}
    for index, lags in samples.items():
        if min(lags) >= config.catch_up_min_lag:
            kinds[index] = "behind"
        elif max(lags) <= config.max_lag_events:
            kinds[index] = "current"
    return kinds


def percentile(values, q):
    ordered_values = sorted(values)
    return ordered_values[min(len(ordered_values) - 1, int(q * (len(ordered_values) - 1) + 0.5))]


def measure(run):
    config = run.config
    batches, progress, source = collect(run)
    (run.folder / "consumer-batches.jsonl").write_text("".join(json.dumps(run.clean(r)) + "\n" for r in batches))
    (run.folder / "consumer-progress.jsonl").write_text("".join(json.dumps(run.clean(r)) + "\n" for r in progress))
    if not batches:
        return {"skipped": True, "reason": f"no '{BATCH}' lines in the Aggregator logs ({source})"}

    width = config.window_seconds
    kinds = classify(progress, config)
    events = collections.Counter()
    by_window = collections.defaultdict(list)
    for b in batches:
        index = int(epoch(b) // width)
        events[index] += b["events"]
        by_window[index].append(b)
    behind = sorted(i for i, kind in kinds.items() if kind == "behind")
    # A full batch means events were waiting, i.e. a short backlog (a restart's catch-up
    # is often over between two lag samples). That window and the next aren't live.
    full = {int(epoch(b) // width) for b in batches if b["events"] >= 0.9 * config.satellite_batch_size}
    current = sorted(i for i, kind in kinds.items()
                     if kind == "current" and i not in full and i - 1 not in full)

    rates = [events[i] / width for i in behind]
    capacity = percentile(rates, 0.5) if len(rates) >= config.min_catch_up_windows else None
    commit = fit(batches)
    estimated = None
    if commit:
        estimated = config.satellite_batch_size / (
            (commit["fixed_ms"] + commit["per_event_ms"] * config.satellite_batch_size) / 1000)

    live_seconds = len(current) * width
    average = sum(events[i] for i in current) / live_seconds if live_seconds else 0.0
    # Busiest peak window made only of current windows.
    per_peak = max(1, round(config.peak_window_seconds / width))
    peak = 0.0
    current_set = set(current)
    for i in current:
        group = range(i, i + per_peak)
        if all(j in current_set for j in group):
            peak = max(peak, sum(events[j] for j in group) / (per_peak * width))
    observed = getattr(run, "live_events_per_second", None)
    if not current:
        average = observed or 0.0
    peak = max(peak, observed or 0.0, average)

    def latencies(indexes):
        return [b["ingest_latency_ms"] for i in indexes for b in by_window[i] if b["ingest_latency_ms"] is not None]

    result = {
        "log_source": source, "batches_logged": len(batches), "progress_samples": len(progress),
        "pods": len({b["pod"] for b in batches}), "batch_size": config.satellite_batch_size,
        "first_log": batches[0]["time"], "last_log": batches[-1]["time"],
        "capacity_events_per_second": round(capacity, 1) if capacity else None,
        "capacity_source": ("measured catch-up (median)" if capacity else
                            "commit times only (optimistic)" if estimated else None),
        "capacity_p10_events_per_second": round(percentile(rates, 0.1), 1) if capacity else None,
        "capacity_p90_events_per_second": round(percentile(rates, 0.9), 1) if capacity else None,
        "catch_up_seconds": len(behind) * width,
        "catch_up_events": sum(events[i] for i in behind),
        "commit_fixed_ms": commit["fixed_ms"] if commit else None,
        "commit_per_event_ms": commit["per_event_ms"] if commit else None,
        "capacity_from_commit_times": round(estimated, 1) if estimated else None,
        "live_seconds_observed": live_seconds,
        "live_average_events_per_second": round(average, 2),
        f"live_peak_{config.peak_window_seconds:.0f}s_events_per_second": round(peak, 2),
        "live_p95_ingest_latency_ms": p95(latencies(current)),
        "backlog_p95_ingest_latency_ms": p95(latencies(behind)),
    }
    run.consumer_stats = {**result, "live_peak": peak}
    if not capacity:
        output("WARN", f"No catch-up in the logs ({source})",
               f"Fewer than {config.min_catch_up_windows} windows of {width:g} s with the lag at "
               f"{config.catch_up_min_lag}+ events. Capacity is estimated from commit times only "
               "(it leaves out reading time), or the forecast is skipped. For a measured number, run P4 "
               f"within {config.loki_lookback_hours:g} h of a catch-up (a fresh task up replays the feed "
               "from offset 0 at full speed; so does a lab that was off for a while).")
    return result
