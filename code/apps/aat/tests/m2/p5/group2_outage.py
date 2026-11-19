"""Group 2 — sensor outage: criteria 3 (other sources unaffected), 4 (no request
spike, fast answers, automatic recovery) and 6 (recovery without intervention)."""
import time

from common import output, p95, require


def sensor(run):
    return run.aggregator("/internal/sensor/status")


def outage(run):
    media = run.login("public")
    before = sensor(run)["sensor"]["requests"]
    run.cluster.kubectl("scale", "deployment/sensor-pantai", "--replicas=0")
    stopped = time.monotonic()
    output("INFO", "Scaled deployment/sensor-pantai to 0 replicas")
    try:
        opened = run.wait("the circuit breaker to open",
                          lambda: (s := sensor(run))["sensor"]["breaker"] in ("open", "half_open") and s, 120)
        opened_after = time.monotonic() - stopped
        time.sleep(run.config.outage_seconds)
        during = sensor(run)
        seconds = time.monotonic() - stopped
        sent = during["sensor"]["requests"] - before
        rate = sent / seconds
        require(rate <= run.config.max_requests_per_second_during_outage,
                f"{sent} requests in {seconds:.0f}s of outage ({rate:.2f}/s): no back-off")
        require(not during["available"], "the sensor is still reported available during the outage")

        # Clients are answered from stored data, quickly, with the sensor marked unavailable.
        coastal_ms, other_ms = [], []
        for _ in range(10):
            _, page, ms = run.call("client", "/hazards?source=SENSOR_PANTAI&limit=5", token=media)
            coastal_ms.append(ms)
            other_ms.append(run.call("client", "/hazards?limit=5", token=media)[2])
        status = next((s for s in page["sources"] if s["source"] == "SENSOR_PANTAI"), None)
        require(status and status["stale"], "client responses do not mark SENSOR_PANTAI unavailable")
        for label, values in (("SENSOR_PANTAI", coastal_ms), ("all sources", other_ms)):
            require(p95(values) < run.config.client_p95_ms_during_outage,
                    f"client p95 for {label} is {p95(values):.0f} ms during the outage")
    finally:
        run.cluster.kubectl("scale", "deployment/sensor-pantai", "--replicas=1")
        output("INFO", "Scaled deployment/sensor-pantai back to 1 replica; no further action until recovery")

    restarted = time.monotonic()
    recovered = run.wait("automatic recovery (breaker closed, fresh data)",
                         lambda: (s := sensor(run))["available"] and s["sensor"]["breaker"] == "closed" and s,
                         run.config.recovery_timeout)
    return {"breaker_opened_after_s": round(opened_after, 1), "last_error": opened["sensor"]["last_error"],
            "outage_s": round(seconds), "requests_during_outage": sent, "requests_per_s": round(rate, 2),
            "client_p95_ms_sensor": round(p95(coastal_ms), 1), "client_p95_ms_all_sources": round(p95(other_ms), 1),
            "client_sensor_status": status["status"], "recovered_after_s": round(time.monotonic() - restarted, 1),
            "recovered_breaker": recovered["sensor"]["breaker"]}


def run_group(run):
    if not run.config.outage:
        run.skip("sensor-outage-and-recovery", "CONFIG.outage is False")
        return
    input(f"Enter → stop Sensor Pantai for {run.config.outage_seconds:.0f}s and watch the breaker and recovery: ")
    r = run.stage("sensor-outage-and-recovery", outage, run)
    output("RESULT", "Outage", f"Breaker opened after {r['breaker_opened_after_s']}s",
           f"Requests to the sensor: {r['requests_during_outage']} in {r['outage_s']}s ({r['requests_per_s']}/s)",
           f"Client p95: sensor {r['client_p95_ms_sensor']} ms, all sources {r['client_p95_ms_all_sources']} ms "
           f"(sensor shown as {r['client_sensor_status']})",
           f"Recovered on its own after {r['recovered_after_s']}s")
