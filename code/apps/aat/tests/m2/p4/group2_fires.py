"""Group 2 — fires: criteria 1 (Definisi Kebakaran, one event per fire across
satellites), 2 (late/excluded detections counted, not dropped) and 3 (updates and
retractions change the existing event), plus the Audit API comparison."""
import datetime as dt
import re

from common import output, parse_time, require

# Coordinates are kept exactly as the feed wrote them (updates and retractions name a
# detection by that text), and the feed uses JSON numbers, so near the equator a
# latitude can be in exponent form, e.g. "N20|2026-11-30|1016|-9.0e-5|111.20328".
NUM = r"-?\d+(\.\d+)?([eE][+-]?\d+)?"
KEY = re.compile(rf"^[^|]+\|\d{{4}}-\d{{2}}-\d{{2}}\|\d{{1,4}}\|{NUM}\|{NUM}$")


def expected_severity(count, frp):
    if count >= 20 or frp >= 100:
        return "AWAS"
    if count >= 6 or frp >= 30:
        return "SIAGA"
    return "WASPADA"


def detection_time(key):
    _, date, hhmm, _, _ = key.split("|")
    return dt.datetime.strptime(f"{date} {int(hhmm):04d}", "%Y-%m-%d %H%M").replace(tzinfo=dt.timezone.utc)


def mapping(run):
    fires = run.hazards("SATELIT")
    require(fires, "no SATELIT HazardEvents stored yet")
    refs, owner, active, retired, multi = set(), {}, 0, 0, 0
    # occurred_at is feed (virtual) time and ingested_at is wall time; the feed's
    # clock can run ahead of the wall, so bound event time by the feed's own clock.
    vnow = (run.aggregator("/internal/satellite/stats").get("feed") or {}).get("virtual_now")
    require(vnow, "virtual_now not reported by /internal/satellite/stats")
    feed_now = parse_time(vnow)
    for h in fires:
        a, hid = h["attributes"], h["hazard_id"]
        require(h["source"] == "SATELIT" and h["hazard_type"] == "WILDFIRE", f"{hid}: wrong source/type")
        require(h["source_ref_id"] not in refs, f"duplicate source_ref_id {h['source_ref_id']}")
        refs.add(h["source_ref_id"])
        require(h["area_name"], f"{hid}: no area_name")
        require(parse_time(h["occurred_at"]) <= feed_now, f"{hid}: occurred after the feed's virtual_now {vnow}")
        keys = a.get("detection_keys") or []
        bad = [k for k in keys if not KEY.match(k)]
        require(not bad, f"{hid}: malformed detection key(s) {bad[:3]}")
        require(a.get("detection_count") == len(keys), f"{hid}: detection_count differs from its keys")
        if h["severity"] == "NORMAL":
            # Criterion 3: a fire that shrank, merged, or was retracted keeps its event.
            require(a.get("status") in ("dissolved", "merged"), f"{hid}: NORMAL without dissolved/merged status")
            retired += 1
            continue
        require(len(keys) >= 2, f"{hid}: a fire needs at least two detections")
        require(sorted(a["satellites"]) == sorted({k.split("|")[0] for k in keys}), f"{hid}: satellites list wrong")
        require(a.get("max_confidence") in ("n", "h"), f"{hid}: includes a low-confidence detection")
        want = expected_severity(a["detection_count"], a["total_frp_mw"])
        require(h["severity"] == want, f"{hid}: severity {h['severity']}, the rules give {want}")
        require(parse_time(h["occurred_at"]) == min(detection_time(k) for k in keys),
                f"{hid}: occurred_at is not its earliest detection")
        for k in keys:
            require(k not in owner, f"detection {k} is in two fires ({owner.get(k)}, {hid})")
            owner[k] = hid
        multi += len(a["satellites"]) > 1
        active += 1
    run.fire_refs = refs
    return {"events": len(fires), "active_fires": active, "retired_fires": retired,
            "fires_seen_by_several_satellites": multi, "detections_in_fires": len(owner)}


def counted(run):
    s = run.aggregator("/internal/satellite/stats")
    fields = ("fires", "single_detections", "late_detections", "low_confidence_detections", "retracted_detections")
    for field in fields:
        require(isinstance(s.get(field), int), f"{field} is not reported")
    return {field: s[field] for field in fields}


def audit(run):
    token = run.env.get("AUDIT_TOKEN")
    require(token, "AUDIT_TOKEN missing from the env file")
    _, result, _ = run.call("aggregator", "/internal/audit/checksum?source=SATELIT", token=token)
    stored = len(run.hazards("SATELIT"))
    require(not result.get("partial"), "Audit API reports a partial result")
    # The feed keeps writing, so allow the events added between the two reads.
    require(abs(result["count"] - stored) <= run.config.max_lag_events,
            f"audit count {result['count']} vs stored {stored}")
    return {"audit_count": result["count"], "stored_events": stored, "checksum": result["checksum"]}


def run_group(run):
    input("Enter → verify every WILDFIRE event against the fire definition and mapping rules: ")
    result = run.stage("wildfire-mapping", mapping, run)
    output("RESULT", "Fires", f"Events: {result['events']} ({result['active_fires']} active, {result['retired_fires']} retired)",
           f"Seen by several satellites: {result['fires_seen_by_several_satellites']}",
           f"Detections inside fires: {result['detections_in_fires']}")
    run.stage("excluded-detections-counted", counted, run)
    if run.config.compare_audit_checksum:
        input("Enter → check internal Audit/store consistency (not the assistants reference): ")
        run.stage("internal-audit-consistency", audit, run)
    else:
        run.skip("audit-checksum", "CONFIG.compare_audit_checksum is False")
