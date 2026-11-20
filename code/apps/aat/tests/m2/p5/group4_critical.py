"""Group 4 — critical lane: criterion 1 (every critical reading reaches the Field Team
notifier, p95 <= 5 s). Runs last so it never holds up the other checks.

The practice scenario sends its critical readings in the first sensor-hours after the
sensor container starts (about 30, 90 and 150 s of real time at speed 60), and every
restart replays the same readings. Re-sent readings are deduplicated, so they alert
once, ever. The check therefore:

1. looks in the database: critical readings ingested while the current field-notifier
   was logging are measured at once (a fresh lab), with no restart;
2. otherwise restarts the sensor to replay its scenario and waits up to
   CONFIG.wait_critical_seconds for new critical readings, and measures them;
3. if the restart brought only already-stored readings, it is skipped (not passed),
   with evidence that the replay created no duplicate events or alerts. Proving the
   p95 then needs a SENSOR_PANTAI store that has not seen the scenario yet.
"""
import datetime as dt

from common import json_logs, output, p95, parse_time, require

ALERT_MESSAGES = ("critical field alert recorded", "critical field alert sent")


def criticals(run):
    return [h for h in run.hazards("SENSOR_PANTAI") if h["attributes"].get("critical")]


def alert_records(run):
    return list(json_logs(run.cluster.logs("field-notifier"), *ALERT_MESSAGES))


def measure(run, ids, window_start, how):
    def alerted():
        records = alert_records(run)
        return records if ids <= {r["hazard_id"] for r in records} else None
    records = run.wait("an alert for every critical reading", alerted, 60)
    latencies = [r["alert_latency_ms"] for r in records if r["hazard_id"] in ids]
    worst = p95(latencies)
    require(worst <= run.config.alert_p95_ms, f"critical alert p95 {worst} ms exceeds {run.config.alert_p95_ms:.0f} ms")
    return {"how": how, "window_start": window_start, "critical_readings": len(ids), "alerted": len(ids),
            "lost": 0, "p95_ms": worst, "max_ms": max(latencies)}


def critical_alerts(run):
    first = next(json_logs(run.cluster.logs("field-notifier")), None)
    require(first and first.get("time"), "field-notifier has no structured logs")
    since = parse_time(first["time"])

    # 1. Database check: criticals that arrived while this notifier was logging.
    stored = criticals(run)
    current = {h["hazard_id"] for h in stored if parse_time(h["ingested_at"]) >= since}
    if current:
        return measure(run, current, first["time"], "already ingested during this field-notifier run")
    stored_ids = {h["hazard_id"] for h in stored}
    output("INFO", "Database check", f"{len(stored_ids)} critical readings stored, none since the current "
           f"field-notifier started ({first['time']}); restarting the sensor to replay its scenario")

    # 2. Restart the sensor; its scenario starts over and re-sends its critical readings.
    alerts_before = {r["hazard_id"] for r in alert_records(run)} & stored_ids
    restarted = dt.datetime.now(dt.timezone.utc)
    run.cluster.kubectl("rollout", "restart", "deployment/sensor-pantai")
    run.cluster.kubectl("rollout", "status", "deployment/sensor-pantai", "--timeout=120s", timeout=150)
    output("INFO", "Restarted deployment/sensor-pantai",
           f"waiting up to {run.config.wait_critical_seconds:.0f}s for its critical readings")

    def new_criticals():
        found = {h["hazard_id"] for h in criticals(run)
                 if h["hazard_id"] not in stored_ids and parse_time(h["ingested_at"]) >= restarted}
        return found or None
    try:
        fresh = run.wait("a new critical reading after the sensor restart", new_criticals, run.config.wait_critical_seconds)
    except ValueError:
        fresh = None
    if fresh:
        return measure(run, fresh, restarted.isoformat(), "new after the sensor restart")

    # 3. Only replays: show they were deduplicated, and record why p95 is not proven here.
    after = criticals(run)
    after_ids = [h["hazard_id"] for h in after]
    require(len(after_ids) == len(set(after_ids)), "the replay created duplicate critical events")
    alerts = [r["hazard_id"] for r in alert_records(run) if r["hazard_id"] in stored_ids]
    require(len(alerts) == len(set(alerts)), "a replayed critical reading was alerted twice")
    return {"skipped": True,
            "reason": (f"the sensor restart replayed only the {len(stored_ids)} critical readings already stored; "
                       "they were deduplicated and alert once, so p95 needs a SENSOR_PANTAI store that has not "
                       "seen the scenario yet (fresh lab or first run on the VM cluster)"),
            "stored_critical_readings": len(stored_ids), "new_critical_readings": 0,
            "duplicate_events": 0, "duplicate_alerts": 0,
            "alerts_seen_by_this_notifier_before_restart": len(alerts_before)}


def run_group(run):
    input("Enter → check the database, restart Sensor Pantai and verify its critical readings reach the Field Team: ")
    alerts = run.stage("critical-alerts-delivered", critical_alerts, run)
    if not alerts.get("skipped"):
        output("RESULT", "Critical lane", f"{alerts['alerted']}/{alerts['critical_readings']} alerted, 0 lost "
               f"({alerts['how']})", f"p95 {alerts['p95_ms']} ms (target {run.config.alert_p95_ms:.0f} ms)")
