"""Group 4 — criterion 4 forecast: simulate the 60x burst with this cluster's own
consumer numbers (measured in group 1 from the Aggregator logs).

This is a model, not the exam: the real burst only runs in the exam session
(POST /v1/exam/start with the assistants' exam token) and is shown on the Grafana
'P4 Feed Satelit' panels. The model follows satellite.go: one owner commits one
batch at a time, a batch is flushed at batch_size events or every 0.5 s, and a batch
of n events takes fixed + per_event * n seconds, scaled so that full batches run at
the measured capacity.

Scenarios, each for every CONFIG.burst_seconds:
- realistic: CONFIG.burst_factor x the live *average* rate. A feed sped up 60x
  delivers 60x the volume, so the average sets the sustained rate;
- worst case: CONFIG.burst_factor x the busiest CONFIG.peak_window_seconds of the
  live feed. In practice that minute holds a satellite pass (~900 events in ~10 s),
  so this assumes passes back to back for the whole burst.
"Current" means lag back to <= CONFIG.max_lag_events. The verdict is the realistic
case for CONFIG.verdict_burst_seconds: current again within 60 s. Capacity is the
median catch-up rate measured in group 1 (p10/p90 are printed alongside).
"""
import bisect

from common import output, require

FLUSH_EVERY = 0.5
CURRENT_WITHIN = 60.0


class Arrivals:
    """Piecewise-constant feed rate; offsets arrive evenly within each segment."""

    def __init__(self, segments):
        self.starts, self.counts, self.rates = [], [], []
        t = n = 0.0
        for rate, seconds in segments:
            self.starts.append(t); self.counts.append(n); self.rates.append(rate)
            t += seconds; n += rate * seconds
        self.end, self.total = t, n

    def count(self, t):
        """Events published by time t."""
        i = max(0, bisect.bisect_right(self.starts, t) - 1)
        return min(self.total, self.counts[i] + self.rates[i] * (t - self.starts[i]))

    def time_of(self, k):
        """When event number k (0-based, fractional) was published."""
        i = max(0, bisect.bisect_right(self.counts, k) - 1)
        while i > 0 and self.rates[i] == 0:
            i -= 1
        return self.starts[i] + (k - self.counts[i]) / self.rates[i] if self.rates[i] else self.starts[i]


def simulate(normal, burst, burst_seconds, batch_time, batch_size, current_lag, before=30.0, after=3600.0):
    feed = Arrivals([(normal, before), (burst, burst_seconds), (normal, after)])
    burst_end = before + burst_seconds
    now, done_events, lag_peak, caught = 0.0, 0.0, 0.0, None
    latencies = []  # (seconds, events): end-to-end, feed publish -> committed
    while now < feed.end:
        available = feed.count(now) - done_events
        if available < batch_size:
            full_at = feed.time_of(done_events + batch_size)
            now = min(max(now, full_at), now + FLUSH_EVERY)
            available = feed.count(now) - done_events
        n = min(batch_size, available)
        if n < 1:
            now += FLUSH_EVERY
            continue
        lag = feed.count(now) - done_events
        lag_peak = max(lag_peak, lag)
        if caught is None and now >= burst_end and lag <= current_lag:
            caught = now - burst_end
            break
        finished = now + batch_time(n)
        # Oldest and newest event of the batch bound its latencies; weight by size.
        oldest, newest = feed.time_of(done_events), feed.time_of(done_events + n - 1)
        latencies.append((finished - (oldest + newest) / 2, n))
        done_events += n
        now = finished
    latencies.sort()
    total, seen, p95 = sum(n for _, n in latencies), 0.0, 0.0
    for seconds, n in latencies:
        seen += n
        if seen >= 0.95 * total:
            p95 = seconds
            break
    return {"lag_peak": int(lag_peak), "current_after_seconds": None if caught is None else round(caught, 1),
            "p95_feed_to_stored_seconds": round(p95, 1)}


def forecast(run):
    stats = getattr(run, "consumer_stats", None)
    if not stats:
        return {"skipped": True, "reason": "no consumer statistics (group 1 'consumer-stats' did not run or found no batches)"}
    capacity = stats["capacity_events_per_second"] or stats["capacity_from_commit_times"]
    if not capacity:
        return {"skipped": True, "reason": "consumer capacity unknown: the logs hold no catch-up and too few batch "
                "sizes to fit commit times; run P4 soon after a fresh task up"}
    config, size = run.config, stats["batch_size"]
    fixed, per_event = (stats["commit_fixed_ms"] or 0.0) / 1000, (stats["commit_per_event_ms"] or 0.0) / 1000
    if fixed + per_event <= 0:
        fixed, per_event = 0.0, 1.0
    scale = (size / capacity) / (fixed + per_event * size)
    batch_time = lambda n: scale * (fixed + per_event * n)  # noqa: E731
    normal, peak = stats["live_average_events_per_second"], stats["live_peak"]

    scenarios, table = [], []
    realistic = f"{config.burst_factor:.0f}x live average (realistic)"
    for label, rate in ((realistic, config.burst_factor * normal),
                        (f"{config.burst_factor:.0f}x busiest {config.peak_window_seconds:.0f} s (worst case)",
                         config.burst_factor * peak)):
        for seconds in config.burst_seconds:
            r = simulate(max(normal, 0.01), rate, seconds, batch_time, size, config.max_lag_events)
            ok = r["current_after_seconds"] is not None and r["current_after_seconds"] <= CURRENT_WITHIN
            scenarios.append({"scenario": label, "burst_events_per_second": round(rate, 1), "burst_seconds": seconds,
                              **r, "would_pass": ok})
            current = "> 1 h" if r["current_after_seconds"] is None else f"{r['current_after_seconds']:.0f} s"
            short = "realistic " if label == realistic else "worst case"
            table.append(f"{'PASS' if ok else 'FAIL'}  {short} {rate:6.0f} ev/s for {seconds:4.0f} s → lag peak "
                         f"{r['lag_peak']:>8,}, current after {current:>6}, p95 feed→stored {r['p95_feed_to_stored_seconds']:.1f} s")
    run.save("burst-simulation.json", {"inputs": stats, "scenarios": scenarios})

    burst = config.burst_factor * normal
    longest = (CURRENT_WITHIN * (capacity - normal) / (burst - capacity)) if burst > capacity else None
    spread = (f", p10 {stats['capacity_p10_events_per_second']:.0f} / p90 {stats['capacity_p90_events_per_second']:.0f}"
              if stats.get("capacity_p10_events_per_second") else "")
    output("INFO", f"Burst simulation (capacity {capacity:.0f} ev/s, {stats['capacity_source']}{spread}; "
           f"live {normal:.1f} ev/s, busiest {config.peak_window_seconds:.0f} s {peak:.1f} ev/s)", *table,
           "Longest realistic burst that stays within 60 s: "
           + ("any length (capacity above the burst rate)" if longest is None else f"about {longest:.0f} s"))
    verdict = next(s for s in scenarios if s["burst_seconds"] == config.verdict_burst_seconds
                   and s["scenario"] == realistic)
    result = {"capacity_events_per_second": round(capacity, 1), "capacity_source": stats["capacity_source"],
              "live_average_events_per_second": round(normal, 2), "live_peak_events_per_second": round(peak, 2),
              "burst_events_per_second": round(burst, 1),
              "verdict_burst_seconds": config.verdict_burst_seconds,
              "verdict_current_after_seconds": verdict["current_after_seconds"],
              "verdict_lag_peak": verdict["lag_peak"],
              "verdict_p95_feed_to_stored_seconds": verdict["p95_feed_to_stored_seconds"],
              "longest_burst_within_60s": "any" if longest is None else round(longest),
              "worst_case_passes": all(s["would_pass"] for s in scenarios),
              "note": "simulation with measured inputs, not the exam burst"}
    require(verdict["would_pass"],
            f"simulation predicts a FAIL: a {burst:.0f} ev/s burst for {config.verdict_burst_seconds:.0f} s is "
            f"current again only after {verdict['current_after_seconds'] or '> 3600'} s (limit 60 s) at "
            f"{capacity:.0f} ev/s; see burst-simulation.json")
    return result


def run_group(run):
    if not run.config.simulate_burst:
        run.skip("burst-simulation", "CONFIG.simulate_burst is False")
        return
    input("Enter → simulate the 60x burst with this cluster's measured consumer speed: ")
    result = run.stage("burst-simulation", forecast, run)
    if not result.get("skipped"):
        output("RESULT", "Burst forecast",
               f"{result['burst_events_per_second']:.0f} ev/s for {result['verdict_burst_seconds']:.0f} s → current "
               f"again after {result['verdict_current_after_seconds']} s (limit 60 s)",
               "Worst case also passes" if result["worst_case_passes"] else
               "Some worst-case scenarios fail (see the table above and burst-simulation.json)")
