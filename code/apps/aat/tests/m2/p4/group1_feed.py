"""Group 1 — the live feed: criteria 7 (live processing) and 4 (keeping up),
plus format compatibility (no event rejected)."""
import time

from common import output, require


def stats(run):
    return run.aggregator("/internal/satellite/stats")


def first_offset(run):
    """The consumer has connected and committed at least one batch."""
    def ready():
        s = stats(run)
        return s if s["feed"]["offset"] is not None else None
    try:
        return run.wait("the satellite consumer to commit a first offset", ready, run.config.catch_up_timeout)
    except ValueError:
        logs = run.cluster.logs("aggregator")
        if "too_many_connections" in logs:
            raise ValueError("the feed answers 429 too_many_connections: another consumer holds this group's "
                             "token (only one connection is allowed); stop it, then rerun") from None
        if "satellite feed disabled" in logs:
            raise ValueError("Aggregators run without SATELLITE_GROUP_TOKEN: add it to .env.runtime, "
                             "run task up, then restart deployment/aggregator") from None
        raise


def live_feed(run):
    first = first_offset(run)
    time.sleep(run.config.observe_seconds)
    second = stats(run)
    before, after = first["feed"]["offset"], second["feed"]["offset"]
    require(after > before, f"feed offset did not advance in {run.config.observe_seconds:.0f}s ({before} -> {after})")
    return {"offset_before": before, "offset_after": after, "seconds": run.config.observe_seconds,
            "events_per_second": round((after - before) / run.config.observe_seconds, 1)}


def caught_up(run):
    def small_lag():
        s = stats(run)
        lag = s["feed"].get("lag_events")
        return s if lag is not None and lag <= run.config.max_lag_events else None
    s = run.wait(f"lag <= {run.config.max_lag_events} events", small_lag, run.config.catch_up_timeout)
    require(s["feed"]["rejected_events"] == 0,
            f"{s['feed']['rejected_events']} feed events were rejected: payload format mismatch (see satellite_rejects)")
    return {"offset": s["feed"]["offset"], "latest_offset": s["feed"]["latest_offset"],
            "lag_events": s["feed"]["lag_events"], "rejected_events": 0, "virtual_now": s["feed"]["virtual_now"]}


def run_group(run):
    input("Enter → check the Aggregator is consuming the live satellite feed: ")
    run.stage("live-feed-consumed", live_feed, run)
    input("Enter → wait until the consumer has caught up with the feed: ")
    result = run.stage("caught-up-no-rejects", caught_up, run)
    output("RESULT", "Feed", f"Offset {result['offset']} of {result['latest_offset']} (lag {result['lag_events']})",
           "Rejected events: 0")
