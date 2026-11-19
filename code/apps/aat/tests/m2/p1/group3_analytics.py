"""Group 3 — dashboard aggregates and truthful partial results (criterion 7)."""
import datetime as dt

from helper import output, ConcurrentDataChange, analytics, require
from group1_distribution import availability


def reconcile(events, summary, query):
    availability(summary, "analytics", complete=True)
    require(type(summary.get("total")) is int and summary["total"] >= 0, "analytics total must be a nonnegative integer")
    for field in ("by_type", "by_severity", "by_area"):
        rows = summary.get(field)
        require(isinstance(rows, list) and all(isinstance(row, dict) and isinstance(row.get("key"), str)
                                             and type(row.get("count")) is int and row["count"] >= 0 for row in rows),
                f"{field}: invalid counts")
        require(len({row["key"] for row in rows}) == len(rows), f"{field}: duplicate category")
    timeline = summary.get("timeline")
    require(isinstance(timeline, list), "analytics missing timeline")
    times = []
    for row in timeline:
        counts = row.get("counts")
        require(isinstance(counts, dict) and set(counts) == set(analytics.TYPES)
                and all(type(n) is int and n >= 0 for n in counts.values()), "invalid timeline counts")
        times.append(analytics.timestamp(row["at"]))
    require(all(a < b for a, b in zip(times, times[1:])), "timeline must be ordered without duplicates")
    return analytics.reconcile(events, summary, analytics.timestamp(query["from"]),
                               analytics.timestamp(query["to"]), query["interval"])


def analytic_query(events, interval="month", end=None):
    require(bool(events), "cannot reconcile an empty P1 dataset")
    times = [analytics.timestamp(e["occurred_at"]) for e in events]
    start = min(times).replace(hour=0, minute=0, second=0, microsecond=0)
    end = (analytics.timestamp(end) if end else
           max(times).replace(hour=0, minute=0, second=0, microsecond=0) + dt.timedelta(days=1))
    require(start < end, "analytics_end must be after the first historical event")
    at, buckets = analytics.bucket(start, interval), 0
    while at < end:
        buckets += 1
        require(buckets <= 1000, "analytics range exceeds 1000 buckets; choose a coarser interval")
        at = analytics.next_bucket(at, interval)
    return {"from": start.isoformat(), "to": end.isoformat(), "interval": interval}


def reconcile_stable_window(before, after, summary, query):
    """Exact chart comparison over a historical window unchanged across both exports."""
    start, end = analytics.timestamp(query["from"]), analytics.timestamp(query["to"])
    def selected(events):
        return {event["hazard_id"]: event for event in events
                if start <= analytics.timestamp(event["occurred_at"]) < end}
    first, last = selected(before), selected(after)
    if first != last:
        raise ConcurrentDataChange(
            f"historical chart data changed during measurement ({len(first)} → {len(last)} events); "
            "choose an earlier CONFIG.analytics_end if this window still receives updates"
        )
    return reconcile(list(last.values()), summary, query)


def verify_dashboard_data(run):
    from group2_recovery import read_live_view

    view = read_live_view(run)
    run.save("dashboard-analytics.json", dict(view, browser_evidence_pending=True))
    return view["reconciliation"]


def run_group(run):
    print("\nGroup 3: analytics dashboard — criterion 7", flush=True)
    input("Open the analytics dashboard. Enter → compare all charts with Audit API: ")
    run.stage("dashboard-analytics", verify_dashboard_data, run)
    output("MANUAL", "Manual evidence", "Show the browser charts, date filters, and partial/stale banner. "
        "Group 2's monitors check the analytics API during node loss.")
