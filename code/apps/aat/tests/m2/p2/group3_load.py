"""Criteria 6–8: measured load, scale transitions, throughput and rolling update."""
import json
import os
from pathlib import Path
import re
import subprocess
import threading
import time

from helper import NAMESPACE, output, polling_evidence, require, utc


def metric(summary, name, field="count"):
    return summary.get("metrics", {}).get(name, {}).get("values", {}).get(field, 0)


def failure_message(summary, code):
    failed = [name for name, data in summary.get("metrics", {}).items()
              if any(not value.get("ok", False) for value in data.get("thresholds", {}).values())]
    total = metric(summary, "p2_requests")
    errors = metric(summary, "p2_failures")
    denominator = total - metric(summary, "p2_controlled_rejections")
    rate = f"{100 * errors / denominator:.2f}%" if denominator > 0 else "unavailable"
    return (f"k6 exit {code}; unexpected failures {errors:g}/{denominator:g} ({rate}); "
            f"failed thresholds: {', '.join(failed[:8]) or 'see generator log'}")


def validate_summary(summary, vus, seconds, strict=False):
    total = metric(summary, "p2_requests")
    good = metric(summary, "p2_successes")
    rejected = metric(summary, "p2_controlled_rejections")
    errors = metric(summary, "p2_failures")
    require(total > 0 and good > 0 and total == good + rejected + errors, "invalid load counters")
    require(total > rejected and errors / (total - rejected) < 0.01, "unexpected error rate is >=1%")
    if strict:
        require(errors == 0 and rejected == 0, "rolling update had a failed or rejected request")
    for window in range((seconds + 4) // 5):
        require(metric(summary, f"p2_requests{{window:{window}}}") > 0, "load did not span every measurement window")
        require(metric(summary, f"p2_successes{{window:{window}}}") > 0, "no successful traffic in a measurement window")
        require(metric(summary, f"p2_active_vus{{window:{window}}}", "min") >= vus, "generator did not sustain configured VUs")
    return {"requests": total, "successes": good, "controlled_rejections": rejected, "failures": errors,
            "unexpected_error_denominator": total - rejected, "successes_per_second": good / seconds,
            "seconds": seconds, "vus": vus,
            "pdf_load_minimum_met": vus >= 10_000 and seconds >= 120,
            "cache_results": {name: metric(summary, name) for name in summary.get("metrics", {})
                              if name.startswith("p2_cache_results{result:")},
            "http_status_counts": {name: metric(summary, name) for name in summary.get("metrics", {})
                                   if name.startswith("p2_http_status{status:")},
            "latency": summary.get("metrics", {}).get("p2_latency_ms", {}).get("values", {})}


def load(run, name, action=None, strict=False, seconds=None):
    config = run.config
    seconds = config.load_seconds if seconds is None else seconds
    summary_path = run.folder / (name + "-k6.json")
    env = dict(os.environ, P2_VUS=str(config.load_vus), P2_SECONDS=str(seconds),
               P2_WARMUP=str(config.warmup_seconds), P2_LIMIT=str(config.load_limit), P2_URL=config.url.rstrip("/"),
               P2_HOST=run.host, P2_TOKEN=run.credentials["AGGREGATOR_TOKEN"], P2_RUN_ID=run.run_id,
               P2_SUMMARY=str(summary_path), K6_NO_USAGE_REPORT="true")
    # Keep credentials in the child environment; never in argv, logs or evidence.
    process = subprocess.Popen([config.k6, "run", "--quiet", str(Path(__file__).with_name("load.js"))],
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, env=env)
    started = threading.Event()
    with (run.folder / (name + "-k6.log")).open("w") as log:
        def read():
            written, suppressed = 0, 0
            for line in process.stdout:
                if "P2_MEASUREMENT_START" in line:
                    started.set()
                if written >= 2 << 20:
                    suppressed += 1
                    continue
                # k6 does not print request headers; redact the token defensively.
                line = line.replace(run.credentials["AGGREGATOR_TOKEN"], "[REDACTED]")
                log.write(line)
                written += len(line.encode())
                log.flush()
            if suppressed:
                log.write(f"\nLog capped at 2 MiB; {suppressed} further lines omitted. See k6 summary for counters.\n")
        reader = threading.Thread(target=read, daemon=True)
        reader.start()
        try:
            deadline = time.monotonic() + 90 + config.warmup_seconds
            with run.phase(f"k6 {name}: starting and warming up ({config.warmup_seconds}s warmup)", 90 + config.warmup_seconds):
                while not started.wait(0.2):
                    require(process.poll() is None, "k6 exited before measurement; see generator log")
                    require(time.monotonic() < deadline, "load generator did not enter measurement")
            measurement = time.monotonic()
            with run.phase(f"k6 {name}: measured traffic ({config.load_vus} VUs, {seconds}s)", seconds):
                actions = action(measurement, process) if action else None
                code = process.wait(timeout=seconds + 60)
            summary = json.loads(summary_path.read_text()) if summary_path.is_file() else {}
            require(code == 0, failure_message(summary, code))
            require(summary_path.is_file(), "k6 summary missing")
            result = validate_summary(summary, config.load_vus, seconds, strict)
            result["actions"] = actions
            time.sleep(2)
            run.save(name + "-load-result.json", result)
            require(not run.logs.errors, "log collection interrupted during load: " + "; ".join(run.logs.errors[:3]))
            result["polling_during_load"] = polling_evidence(run.logs.snapshot(), measurement, measurement + seconds, config.poll_interval)
            return result
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            reader.join(timeout=5)


def scaling(run):
    seconds = run.config.scaling_seconds or run.config.load_seconds
    run.replicas(3)
    observations = []
    def actions(start, process):
        observations.append({"replicas": 3, "seconds": 0, "at": utc()})
        previous_ready = start
        for fraction, count in ((1 / 3, 5), (2 / 3, 2)):
            deadline = max(start + seconds * fraction, previous_ready + 5)
            while time.monotonic() < deadline:
                require(process.poll() is None, "generator stopped before scale transition")
                time.sleep(0.2)
            transition = {"replicas": count, "requested_seconds": time.monotonic() - start, "at": utc()}
            observations.append(transition)
            run.save("scale-transitions.json", observations)
            remaining = seconds - (time.monotonic() - start) - 5
            require(remaining > 0, "load window ended before the next scale transition; increase scaling_seconds")
            run.replicas(count, timeout=remaining)
            elapsed = time.monotonic() - start
            transition["ready_seconds"] = elapsed
            run.save("scale-transitions.json", observations)
            require(elapsed < seconds - 5 and process.poll() is None,
                    f"scale to {count} Ready replicas took until {elapsed:.1f}s; measurement lasts {seconds}s, with 5s required after transition; increase scaling_seconds")
            previous_ready = time.monotonic()
        return observations
    result = load(run, "scale-3-5-2", actions, seconds=seconds)
    run.replicas(3)
    result["polling_after_scale"] = run.observe()
    return result


def throughput(run):
    results = {}
    expected = None
    for count in (1, 3):
        run.replicas(count)
        before = run.deployment_state()
        identity = {key: value for key, value in before.items() if key != "nodes"}
        if expected is None:
            expected = identity
            admission = before.get("read_admission", {})
            if admission.get("rps_per_replica", 0) > 0:
                output("INFO", f"Read rate limit: {admission['rps_per_replica']} RPS and "
                       f"{admission['burst_per_replica']} burst per replica in both samples; "
                       "this measures configured admission capacity, not uncapped throughput.")
        require(identity == expected, "Aggregator image or pod configuration changed between throughput samples; check GitOps isolation")
        if count == 3:
            require(len(before.get("nodes", [])) >= 3, "throughput comparison needs three Aggregators on distinct nodes; check topology spread")
        results[count] = load(run, f"throughput-{count}")
        after = run.deployment_state()
        results[count]["deployment_before"] = before
        results[count]["deployment_after"] = after
        run.save(f"throughput-{count}-deployment.json", {"before": before, "after": after})
        require({key: value for key, value in after.items() if key != "nodes"} == expected,
                "Aggregator image or pod configuration changed during throughput measurement; comparison is invalid")
    ratio = results[3]["successes_per_second"] / results[1]["successes_per_second"]
    comparison = {"one": results[1], "three": results[3], "ratio": ratio,
                  "read_admission": expected.get("read_admission"),
                  "capacity_interpretation": "configured admission capacity; not an uncapped capacity measurement"
                  if (expected.get("read_admission") or {}).get("rps_per_replica", 0) > 0 else "no configured read RPS cap",
                  "workload": "same VUs, duration, route, query, gzip negotiation and resource limits; successful JSON responses only"}
    run.save("throughput-comparison.json", comparison)
    require(ratio >= 2, f"3-replica throughput is {ratio:.2f}x baseline, below 2.0x "
            f"(1 replica: {results[1]['successes_per_second']:.2f} successful RPS; "
            f"3 replicas: {results[3]['successes_per_second']:.2f} successful RPS)")
    return comparison


def rolling(run):
    seconds = run.config.scaling_seconds or run.config.load_seconds
    image = run.config.rolling_image
    require(re.fullmatch(r"[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}", image or ""), "rolling_image must be an immutable digest reference")
    run.replicas(3)
    old_ids = {s["imageID"] for p in run.pods() if p["metadata"].get("labels", {}).get("role") == "aggregator"
               for s in p.get("status", {}).get("containerStatuses", [])}
    def action(start, process):
        while time.monotonic() < start + seconds / 3:
            require(process.poll() is None, "generator stopped before rolling update")
            time.sleep(0.2)
        run.kube("set", "image", "deployment/aggregator", "-n", NAMESPACE, "aggregator=" + image, json_output=False)
        remaining = seconds - (time.monotonic() - start) - 5
        require(remaining > 0, "load window ended before rollout; increase scaling_seconds")
        run.replicas(3, timeout=remaining)
        new_ids = {s["imageID"] for p in run.pods() if p["metadata"].get("labels", {}).get("role") == "aggregator"
                   and not p["metadata"].get("deletionTimestamp") for s in p.get("status", {}).get("containerStatuses", [])}
        require(new_ids and not (new_ids & old_ids), "rolling update did not change the actual running image")
        require(time.monotonic() - start < seconds - 5, "rollout finished after measurement; increase scaling_seconds")
        return {"old_image_ids": sorted(old_ids), "new_image_ids": sorted(new_ids), "image": image}
    return load(run, "rolling-update", action, strict=True, seconds=seconds)


def run_group(run):
    config = run.config
    scaling_seconds = config.scaling_seconds or config.load_seconds
    run.report["load_configuration"] = {
        "vus": config.load_vus, "scaling_seconds": scaling_seconds,
        "throughput_seconds_each": config.load_seconds, "warmup_seconds_each": config.warmup_seconds,
        "pdf_sustained_load_minimum_met": config.load_vus >= 10_000 and scaling_seconds >= 120,
    }
    message = f"Group 3: {config.load_vus} VUs; scaling {scaling_seconds}s; throughput 2 x {config.load_seconds}s; warmup {config.warmup_seconds}s per run."
    if config.rolling_image:
        message += f" Optional image rollout adds {scaling_seconds}s plus warmup."
    if not run.report["load_configuration"]["pdf_sustained_load_minimum_met"]:
        message += " Development run: does not meet the PDF sustained-load minimum (10,000 VUs, 120s)."
    run.prompt(message + " It changes replicas and may change the image. Continue only on an isolated test cluster.")
    run.stage("P2.6-scaling-load", lambda: scaling(run))
    run.stage("P2.7-throughput-ratio", lambda: throughput(run))
    if run.config.rolling_image:
        run.stage("P2.8-rolling-update", lambda: rolling(run))
    else:
        run.skip("P2.8-rolling-update", "set CONFIG.rolling_image to a different available immutable image digest")
