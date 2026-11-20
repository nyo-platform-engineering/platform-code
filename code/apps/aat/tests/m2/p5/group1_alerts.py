"""Group 1 — readings: criterion 2 (contract mapping; unprocessable data kept for
BNPB Internal Ops only). The critical lane (criterion 1) is Group 4, run last."""
from common import output, require


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
    run.stage("parked-data-visible-to-ops-only", ops_only, run)
