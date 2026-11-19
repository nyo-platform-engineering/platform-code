"""Group 1 — readings and alerts: criterion 1 (every critical reading reaches the
Field Team notifier, p95 <= 5 s) and 2 (contract mapping; unprocessable data kept
for BNPB Internal Ops only)."""
from common import json_logs, output, p95, parse_time, require

ALERT_MESSAGES = ("critical field alert recorded", "critical field alert sent")


def sensor(run):
    return run.aggregator("/internal/sensor/status")


def poller_live(run):
    status = run.wait("the sensor poller to fetch fresh data",
                      lambda: (s := sensor(run))["available"] and s, run.config.startup_timeout)
    require(status["stations"] >= 1, "no station list stored")
    return {"stations": status["stations"], "breaker": status["sensor"]["breaker"],
            "requests_so_far": status["sensor"]["requests"]}


def mapping(run):
    events = run.wait("COASTAL HazardEvents", lambda: run.hazards("SENSOR_PANTAI") or None, run.config.startup_timeout)
    critical = suspect = 0
    for h in events:
        a, hid = h["attributes"], h["hazard_id"]
        require(h["hazard_type"] == "COASTAL" and h["area_name"], f"{hid}: bad COASTAL event")
        for field in ("station_id", "sea_level_anomaly_m", "wave_height_m", "quality", "critical"):
            require(field in a, f"{hid}: attribute {field} missing")
        require(a["quality"] in ("ok", "suspect"), f"{hid}: stored with quality {a['quality']}")
        if a["critical"]:
            require(h["severity"] == "AWAS", f"critical reading {hid} is {h['severity']}, not AWAS")
            critical += 1
        elif a["quality"] == "suspect":
            require(h["severity"] != "SIAGA", f"suspect reading {hid} raised SIAGA on its own values")
            suspect += 1
    refs = [h["source_ref_id"] for h in events]
    require(len(refs) == len(set(refs)), "re-sent readings created duplicate events")
    return {"events": len(events), "critical": critical, "suspect": suspect}


def critical_alerts(run):
    logs = run.cluster.logs("field-notifier")
    first = next(json_logs(logs), None)
    require(first and first.get("time"), "field-notifier has no structured logs")
    since = parse_time(first["time"])

    # Alerts from earlier pods left with their logs, so compare only readings that
    # arrived while the current notifier was logging.
    def window():
        found = {h["hazard_id"] for h in run.hazards("SENSOR_PANTAI")
                 if h["attributes"].get("critical") and parse_time(h["ingested_at"]) >= since}
        return found or None
    try:
        criticals = run.wait("a critical reading during this notifier run", window, run.config.wait_critical_seconds)
    except ValueError:
        return {"skipped": True, "reason": f"no new critical reading within {run.config.wait_critical_seconds:.0f}s"}

    def alerted():
        records = list(json_logs(run.cluster.logs("field-notifier"), *ALERT_MESSAGES))
        return records if criticals <= {r["hazard_id"] for r in records} else None
    records = run.wait("an alert for every critical reading", alerted, 60)
    latencies = [r["alert_latency_ms"] for r in records if r["hazard_id"] in criticals]
    worst = p95(latencies)
    require(worst <= run.config.alert_p95_ms, f"critical alert p95 {worst} ms exceeds {run.config.alert_p95_ms:.0f} ms")
    return {"window_start": first["time"], "critical_readings": len(criticals), "alerted": len(criticals),
            "lost": 0, "p95_ms": worst, "max_ms": max(latencies)}


def ops_only(run):
    analyst, media = run.login("analyst"), run.login("public")
    _, page, _ = run.call("client", "/ops/unprocessed?source=SENSOR_PANTAI", token=analyst)
    run.call("client", "/ops/unprocessed?source=SENSOR_PANTAI", token=media, expected=(401, 403))
    items = page.get("items") or []
    return {"parked_or_retrying": len(items), "statuses": sorted({i.get("status") for i in items})}


def run_group(run):
    input("Enter → check the sensor poller and the COASTAL mapping rules: ")
    run.stage("sensor-poller-live", poller_live, run)
    result = run.stage("coastal-mapping", mapping, run)
    output("RESULT", "Readings", f"Events: {result['events']}; critical: {result['critical']}; suspect: {result['suspect']}")
    input("Enter → verify every critical reading reached the Field Team critical lane: ")
    alerts = run.stage("critical-alerts-delivered", critical_alerts, run)
    if alerts.get("skipped"):
        run.skip("critical-alerts-delivered", alerts["reason"] + "; raise CONFIG.wait_critical_seconds")
        run.save("result.json", run.report)
    else:
        output("RESULT", "Critical lane", f"{alerts['alerted']}/{alerts['critical_readings']} alerted, 0 lost",
               f"p95 {alerts['p95_ms']} ms (target {run.config.alert_p95_ms:.0f} ms)")
    run.stage("parked-data-visible-to-ops-only", ops_only, run)
