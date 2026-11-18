#!/usr/bin/env python3
"""Reconcile canonical analytics with a complete Audit API export on stable data."""
import argparse
from collections import Counter
import datetime as dt
import json
from pathlib import Path
import sys

from analytics_http import API, checksum

TYPES = ("SEISMIC", "TSUNAMI", "VOLCANIC", "WILDFIRE", "COASTAL")
SEVERITIES = ("NORMAL", "WASPADA", "SIAGA", "AWAS")


def timestamp(value):
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamps need an explicit timezone")
    return parsed.astimezone(dt.timezone.utc)


def bucket(at, interval):
    at = at.astimezone(dt.timezone.utc)
    if interval == "hour":
        return at.replace(minute=0, second=0, microsecond=0)
    start = at.replace(hour=0, minute=0, second=0, microsecond=0)
    if interval == "month":
        return start.replace(day=1)
    if interval == "year":
        return start.replace(month=1, day=1)
    return start - dt.timedelta(days=start.weekday()) if interval == "week" else start


def next_bucket(at, interval):
    if interval == "year":
        return at.replace(year=at.year + 1)
    if interval == "month":
        return at.replace(year=at.year + 1, month=1) if at.month == 12 else at.replace(month=at.month + 1)
    return at + (dt.timedelta(hours=1) if interval == "hour" else dt.timedelta(days=7 if interval == "week" else 1))


def reconcile(events, summary, start, end, interval):
    if summary.get("partial"):
        raise RuntimeError("analytics is partial; verification is incomplete")
    types = Counter({kind: 0 for kind in TYPES})
    severity = Counter({level: 0 for level in SEVERITIES})
    areas, times = Counter(), {}
    selected = []
    for event in events:
        at = timestamp(event["occurred_at"])
        if not start <= at < end:
            continue
        selected.append(event)
        kind = event["hazard_type"]
        kinds = [kind]
        warnings = event["attributes"].get("tsunami_warnings")
        if kind == "SEISMIC" and isinstance(warnings, list) and warnings:
            kinds.append("TSUNAMI")
        for key in kinds:
            types[key] += 1
            times.setdefault(bucket(at, interval), Counter({k: 0 for k in TYPES}))[key] += 1
        severity[event["severity"]] += 1
        areas[event["area_name"]] += 1
    if summary.get("total") != len(selected):
        raise ValueError("canonical total disagrees with Audit API (ensure the dataset is stable)")
    for field, expected in (("by_type", types), ("by_severity", severity), ("by_area", areas)):
        actual = {item["key"]: item["count"] for item in summary[field]}
        if actual != dict(expected):
            raise ValueError(f"{field} disagrees with Audit API")
    at = bucket(start, interval)
    expected_times = {}
    while at < end:
        expected_times[at] = dict(times.get(at, Counter({key: 0 for key in TYPES})))
        at = next_bucket(at, interval)
    actual_times = {timestamp(item["at"]): item["counts"] for item in summary["timeline"]}
    if actual_times != expected_times:
        raise ValueError("timeline disagrees with Audit API")
    return {"selected_events": len(selected), "by_type": dict(types), "by_severity": dict(severity), "areas": len(areas), "buckets": len(actual_times), "matched": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:8083")
    parser.add_argument("--env-file", type=Path)
    parser.add_argument("--from", dest="start", required=True, type=timestamp)
    parser.add_argument("--to", dest="end", required=True, type=timestamp)
    parser.add_argument("--interval", choices=("hour", "day", "week", "month", "year"), default="day")
    parser.add_argument("--source", choices=("BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI"))
    parser.add_argument("--expected-count", type=int, help="expected total across the entire audited source, before the occurrence-time filter")
    parser.add_argument("--expected-checksum", help="assistant reference checksum for the entire audited source")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--page-size", type=int, default=200, help="audit page size; lower it for events with large attributes")
    args = parser.parse_args()
    if args.end <= args.start:
        parser.error("--to must be after --from")
    if not 1 <= args.page_size <= 1000:
        parser.error("--page-size must be 1..1000")
    api = API(args.url, args.env_file)
    source = {"source": args.source} if args.source else {}
    query = dict(source, **{"from": args.start.isoformat(), "to": args.end.isoformat(), "interval": args.interval})
    summary = api.request("/internal/analytics", "AGGREGATOR_TOKEN", query)
    if summary.get("partial"):
        raise RuntimeError("analytics is partial; verification is incomplete")
    events, seen, cursor = [], set(), None
    previous = None
    for _ in range(1000):
        params = dict(source, limit=args.page_size)
        if cursor:
            params["cursor"] = cursor
        page = api.request("/internal/audit/events", "AUDIT_TOKEN", params)
        if page.get("partial"):
            raise RuntimeError("audit is partial; verification is incomplete")
        for event in page["items"]:
            if event["hazard_id"] in seen:
                raise ValueError("duplicate ID during export; retry with a stable dataset")
            key = (timestamp(event["ingested_at"]), event["hazard_id"])
            if previous is not None and key <= previous:
                raise ValueError("audit order changed during export; retry with a stable dataset")
            if args.source and event["source"] != args.source:
                raise ValueError("audit returned an unexpected source")
            previous = key
            seen.add(event["hazard_id"])
            events.append(event)
        cursor = page["next_cursor"]
        if cursor is None:
            break
    else:
        raise ValueError("audit export exceeded 1000 pages")
    audit_sum = api.request("/internal/audit/checksum", "AUDIT_TOKEN", source)
    actual_hash = checksum([e["source_ref_id"] for e in events])
    if audit_sum.get("partial") or audit_sum["count"] != len(events) or audit_sum["checksum"] != actual_hash:
        raise ValueError("audit checksum changed or disagrees with export; retry with a stable dataset")
    if args.expected_count is not None and args.expected_count != len(events):
        raise ValueError("event count disagrees with reference")
    if args.expected_checksum is not None and args.expected_checksum != actual_hash:
        raise ValueError("checksum disagrees with reference")
    result = reconcile(events, summary, args.start, args.end, args.interval)
    result.update(audited_events=len(events), checksum=actual_hash, source=args.source, from_utc=args.start.isoformat(), to_utc=args.end.isoformat(), audit_ordered=True, reference_checked=args.expected_count is not None or args.expected_checksum is not None)
    if args.output:
        args.output.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError, KeyError, TypeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
