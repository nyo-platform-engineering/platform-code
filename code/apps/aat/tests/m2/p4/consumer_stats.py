"""Consumer statistics from the Aggregators' own "satellite batch committed" logs.

Every committed batch logs its size (events), commit time (commit_ms), the arrival
of its oldest event to commit (ingest_latency_ms) and the feed offset. From those:

- capacity: events/s while the consumer was behind (runs of full batches back to
  back, e.g. the catch-up after task up or after a restart). That is the real speed
  of this cluster, reading and committing included;
- commit cost: commit_ms = fixed + per_event * events (least squares), used when no
  catch-up is in the logs (optimistic: it leaves out reading time);
- live rate: events/s after the last catch-up, average and the busiest window.

Pod logs disappear with their pods, so this is read before group 3 deletes them.
"""
import collections
import json
import re

from common import json_logs, output, p95, parse_time

MESSAGE = "satellite batch committed"
POD = re.compile(r"^\[pod/([^/\]]+)")


def batches(run):
    text = run.cluster.kubectl("logs", "-l", "app.kubernetes.io/instance=aggregator", "--tail=-1",
                               "--prefix=true", "--max-log-requests=10", timeout=120)
    rows = []
    for line in text.splitlines():
        for record in json_logs(line, MESSAGE):
            match = POD.match(line)
            rows.append({"pod": match.group(1) if match else "", "time": record["time"],
                         "offset": record.get("offset"), "events": record.get("events", 0),
                         "commit_ms": record.get("commit_ms", 0),
                         "ingest_latency_ms": record.get("ingest_latency_ms")})
    rows.sort(key=lambda r: (parse_time(r["time"]), r["offset"] or 0))
    return rows


def fit(rows):
    """commit_ms = fixed + per_event * events, or None without a spread of sizes."""
    xs, ys = [r["events"] for r in rows], [r["commit_ms"] for r in rows]
    if len(rows) < 5 or max(xs) - min(xs) < 20:
        return None
    mx, my = sum(xs) / len(xs), sum(ys) / len(ys)
    sxx = sum((x - mx) ** 2 for x in xs)
    per_event = max(0.0, sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / sxx)
    return {"fixed_ms": round(max(0.0, my - per_event * mx), 2), "per_event_ms": round(per_event, 3)}


def catch_up_runs(rows, batch_size, min_batches):
    """Back-to-back full batches on one pod: the consumer was behind the feed."""
    runs, current = [], []
    for row in rows:
        full = row["events"] >= 0.9 * batch_size
        if full and current and current[-1]["pod"] == row["pod"]:
            current.append(row)
            continue
        if len(current) >= min_batches:
            runs.append(current)
        current = [row] if full else []
    if len(current) >= min_batches:
        runs.append(current)
    return runs


def sustained(runs):
    events = seconds = 0.0
    for stretch in runs:
        # The first batch's commit started before its log line: count the batches after it.
        events += sum(r["events"] for r in stretch[1:])
        seconds += (parse_time(stretch[-1]["time"]) - parse_time(stretch[0]["time"])).total_seconds()
    return (events / seconds if seconds > 0 else None), events, seconds


def live_rate(rows, window):
    if len(rows) < 2:
        return None
    start, end = parse_time(rows[0]["time"]), parse_time(rows[-1]["time"])
    span = (end - start).total_seconds()
    if span <= 0:
        return None
    buckets = collections.Counter()
    for row in rows[1:]:
        buckets[int((parse_time(row["time"]) - start).total_seconds() // window)] += row["events"]
    return {"seconds": round(span), "average": sum(r["events"] for r in rows[1:]) / span,
            "peak": max(buckets.values()) / window}


def measure(run):
    config = run.config
    rows = batches(run)
    (run.folder / "consumer-batches.jsonl").write_text("".join(json.dumps(run.clean(r)) + "\n" for r in rows))
    if not rows:
        return {"skipped": True, "reason": "no 'satellite batch committed' lines in the Aggregator logs"}
    batch_size = config.satellite_batch_size
    runs = catch_up_runs(rows, batch_size, config.min_catch_up_batches)
    capacity, catch_events, catch_seconds = sustained(runs)
    commit = fit(rows)
    estimated = None
    if commit:
        estimated = batch_size / ((commit["fixed_ms"] + commit["per_event_ms"] * batch_size) / 1000)

    after = rows
    if runs:
        last = parse_time(runs[-1][-1]["time"])
        after = [r for r in rows if parse_time(r["time"]) > last]
    live = live_rate(after, config.peak_window_seconds)
    observed = getattr(run, "live_events_per_second", None)
    average = live["average"] if live else (observed or 0.0)
    peak = max(live["peak"] if live else 0.0, observed or 0.0, average)

    live_latency = [r["ingest_latency_ms"] for r in after if r["ingest_latency_ms"] is not None]
    backlog_latency = [r["ingest_latency_ms"] for stretch in runs for r in stretch if r["ingest_latency_ms"] is not None]
    result = {
        "batches_logged": len(rows), "pods": len({r["pod"] for r in rows}), "batch_size": batch_size,
        "first_log": rows[0]["time"], "last_log": rows[-1]["time"],
        "capacity_events_per_second": round(capacity, 1) if capacity else None,
        "capacity_source": "measured catch-up" if capacity else ("commit times only (optimistic)" if estimated else None),
        "catch_up_events": int(catch_events), "catch_up_seconds": round(catch_seconds, 1),
        "commit_fixed_ms": commit["fixed_ms"] if commit else None,
        "commit_per_event_ms": commit["per_event_ms"] if commit else None,
        "capacity_from_commit_times": round(estimated, 1) if estimated else None,
        "live_seconds_observed": live["seconds"] if live else 0,
        "live_average_events_per_second": round(average, 2),
        f"live_peak_{config.peak_window_seconds:.0f}s_events_per_second": round(peak, 2),
        "live_p95_ingest_latency_ms": p95(live_latency),
        "backlog_p95_ingest_latency_ms": p95(backlog_latency),
    }
    run.consumer_stats = {**result, "live_peak": peak}
    if not capacity:
        output("WARN", "No catch-up in the Aggregator logs",
               "Capacity is estimated from commit times only, which leaves out reading time. "
               "For a measured number, run P4 soon after a fresh task up (the Aggregator then "
               "replays the feed from offset 0 at full speed).")
    return result
