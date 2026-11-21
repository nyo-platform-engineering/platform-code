"""Live-feed processing, bounded backlog observation, and consumer log evidence."""
import time

from common import output, require
from run_progress import Progress
import consumer_stats


def stats(run):
    return run.aggregator("/internal/satellite/stats")


def wait_for_feed(run, stage, description, ready, timeout=None):
    """Keep reporting the latest sample even while a stats request is pending."""
    timeout = run.config.catch_up_timeout if timeout is None else timeout
    with Progress(run.folder, stage, clean=run.clean,
                  writer=lambda message: output("WAIT", message)) as progress:
        with progress.phase(description, timeout):
            last_detail = "waiting for first stats response"
            progress.detail(last_detail)

            def sample():
                nonlocal last_detail
                try:
                    s = stats(run)
                except (RuntimeError, ValueError) as error:
                    progress.detail(f"{last_detail}; stats request failed: {error}")
                    raise
                feed = s["feed"]
                values = {key: feed.get(key) for key in
                          ("offset", "latest_offset", "lag_events", "rejected_events")}
                last_detail = "; ".join(f"{key}={value if value is not None else 'unknown'}"
                                        for key, value in values.items())
                progress.detail(last_detail)
                return s if ready(feed) else None

            try:
                return run.wait(description, sample, timeout)
            finally:
                progress.pulse()


def first_offset(run):
    """The consumer has connected and committed at least one batch."""
    try:
        return wait_for_feed(run, "live-feed-consumed", "the satellite consumer to commit a first offset",
                             lambda feed: feed["offset"] is not None)
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
    started = time.monotonic()
    second = wait_for_feed(run, "live-feed-consumed",
                           f"observe live satellite feed for {run.config.observe_seconds:g}s",
                           lambda _: time.monotonic() - started >= run.config.observe_seconds,
                           timeout=run.config.observe_seconds + 2 * run.config.request_timeout + 5)
    elapsed = time.monotonic() - started
    before, after = first["feed"]["offset"], second["feed"]["offset"]
    require(after is not None and after > before,
            f"feed offset did not advance in {elapsed:.0f}s ({before} -> {after})")
    run.live_events_per_second = (after - before) / elapsed
    return {"offset_before": before, "offset_after": after, "seconds": round(elapsed, 1),
            "events_per_second": round(run.live_events_per_second, 1)}


def observe_backlog(run):
    """Observe activity for a fixed window; initial backlog is not a burst verdict."""
    started, samples = time.monotonic(), []
    seconds = run.config.lag_observe_seconds

    def observed(feed):
        samples.append((time.monotonic(), dict(feed)))
        return time.monotonic() - started >= seconds

    # Allow the final stats request (including its GET retry) to finish after the window.
    timeout = seconds + 2 * run.config.request_timeout + 5
    wait_for_feed(run, "feed-backlog-observation", f"observe feed activity for {seconds:g}s",
                  observed, timeout=timeout)
    valid = [(stamp, feed) for stamp, feed in samples if feed.get("offset") is not None]
    require(len(valid) >= 2, "not enough valid feed offsets to prove live processing during observation")
    before_time, before = valid[0]
    after_time, after = valid[-1]
    elapsed = after_time - before_time
    require(after["offset"] > before["offset"],
            f"feed offset did not advance during observation ({before['offset']} -> {after['offset']})")
    require(elapsed > 0, "feed observation has no measurable duration")
    run.feed_rejected_samples = [feed.get("rejected_events") for _, feed in samples]
    lag = after.get("lag_events")
    current = lag is not None and lag <= run.config.max_lag_events
    if not current:
        output("WARN", "Feed is processing; initial catch-up is not yet proven",
               f"Lag {before.get('lag_events')} -> {lag} events; target <= {run.config.max_lag_events}.",
               "Continuing the suite. Recovery within 60s after a real 60x burst still needs separate evidence.")
    return {"offset_before": before["offset"], "offset_after": after["offset"],
            "latest_offset": after.get("latest_offset"),
            "lag_before": before.get("lag_events"), "lag_after": lag,
            "seconds": round(elapsed, 1),
            "events_per_second": round((after["offset"] - before["offset"]) / elapsed, 1),
            "caught_up": current, "burst_recovery_verified": False}


def no_rejected_events(run):
    """Check format compatibility independently of the size of the backlog."""
    counts = list(run.feed_rejected_samples) + [stats(run)["feed"].get("rejected_events")]
    require(all(isinstance(count, int) and count >= 0 for count in counts),
            "feed rejected-event count is unavailable; cannot prove zero rejected events")
    rejected = max(counts)
    require(rejected == 0,
            f"{rejected} feed events were rejected: payload format mismatch (see satellite_rejects)")
    return {"rejected_events": 0}


def run_group(run):
    input("Enter → check the Aggregator is consuming the live satellite feed: ")
    run.stage("live-feed-consumed", live_feed, run)
    input(f"Enter → observe feed activity and backlog for {run.config.lag_observe_seconds:g}s: ")
    result = run.stage("feed-backlog-observation", observe_backlog, run)
    run.stage("no-rejected-feed-events", no_rejected_events, run)
    output("RESULT", "Feed", f"Offset {result['offset_before']} -> {result['offset_after']}; "
           f"lag {result['lag_before']} -> {result['lag_after']}; rejected events: 0",
           "Initial observation only; this does not certify real 60x burst recovery.")
    input("Enter → record the consumer's statistics (capacity, live rate, ingest latency) from the Aggregator logs: ")
    stats = run.stage("consumer-stats", consumer_stats.measure, run)
    if not stats.get("skipped"):
        capacity = stats["capacity_events_per_second"] or stats["capacity_from_commit_times"]
        output("RESULT", "Consumer",
               (f"Capacity {capacity} ev/s ({stats['capacity_source']})" if capacity else "Capacity not measurable from these logs")
               + f"; live {stats['live_average_events_per_second']} ev/s",
               f"p95 ingest latency live {stats['live_p95_ingest_latency_ms']} ms, "
               f"behind {stats['backlog_p95_ingest_latency_ms']} ms; every batch is in consumer-batches.jsonl")
