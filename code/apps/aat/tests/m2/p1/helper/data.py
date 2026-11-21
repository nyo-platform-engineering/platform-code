"""Catalog loading, audit checks, and independent analytics calculations."""
from collections import Counter
import datetime as dt
import hashlib
import json
import math
import re
import sys
import time

from common import output, require, utc
from .cluster import cluster_topology


def checksum(refs):
    ordered = sorted(refs, key=lambda ref: ref.encode("utf-8"))
    return hashlib.sha256("\n".join(ordered).encode("utf-8")).hexdigest()


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


def progress(label, current, total=None, started=None, detail=""):
    count = str(current) if total is None else f"{current}/{total} ({current / total:.0%})"
    details = [f"Completed: {count}"]
    if started is not None:
        details.append(f"Elapsed: {time.monotonic() - started:.0f}s")
    if detail:
        details.extend(part.strip() for part in detail.split("; "))
    output("PROGRESS", label, *details)


def read_catalog(path):
    if path.stat().st_size > 128 << 20:
        raise ValueError("catalog exceeds 128 MiB; split it into smaller files")
    raw = path.read_text(encoding="utf-8-sig")
    if raw.lstrip().startswith("["):
        records = json.loads(raw)
    else:
        records = [json.loads(line) for line in raw.splitlines() if line.strip()]
    if not isinstance(records, list) or not records:
        raise ValueError("expected a nonempty JSON array or NDJSON of SeismicEvent objects")
    for position, record in enumerate(records, 1):
        if not isinstance(record, dict):
            raise ValueError(f"record {position} must be an object")
        for key in ("event_id", "region_name", "occurred_at"):
            if not isinstance(record.get(key), str) or not record[key].strip():
                raise ValueError(f"record {position}: {key} must be a nonempty string")
        at = dt.datetime.fromisoformat(record["occurred_at"].replace("Z", "+00:00"))
        if at.tzinfo is None:
            raise ValueError(f"record {position}: occurred_at needs an explicit timezone")
        for key, lower, upper in (("epicenter_lat", -90, 90), ("epicenter_lon", -180, 180), ("magnitude", 0, math.inf), ("depth_km", 0, math.inf)):
            value = record.get(key)
            if isinstance(value, bool) or not isinstance(value, (int, float)) or not lower <= value <= upper or abs(value) > sys.float_info.max or not math.isfinite(value):
                raise ValueError(f"record {position}: invalid {key}")
        if not isinstance(record.get("potential_tsunami"), bool):
            raise ValueError(f"record {position}: potential_tsunami must be a boolean")
        # Reject non-JSON numbers even in extra attributes before any batch is sent.
        json.dumps(record, allow_nan=False)
    return records


class DataUnavailable(RuntimeError):
    """A valid partial response cannot yet be used for a complete checkpoint."""


class ConcurrentDataChange(ValueError):
    """An exact comparison must be retried because its selected data changed."""


def read_reference(path):
    reference = json.loads(path.read_text())
    valid_count = type(reference.get("count")) is int and reference["count"] > 0
    digest = reference.get("checksum")
    valid_digest = isinstance(digest, str) and re.fullmatch("[0-9a-f]{64}", digest)
    require(
        reference.get("source") == "BMKG" and valid_count and valid_digest,
        "reference must have source=BMKG, positive count and lowercase SHA-256 checksum",
    )
    scope = reference.get("scope", "source")
    require(scope in ("source", "catalog"), "reference scope must be source or catalog")
    if scope == "catalog":
        require(
            reference.get("dataset_origin") == "development_mock"
            and reference.get("official") is False
            and isinstance(reference.get("source_ref_prefix"), str)
            and bool(reference["source_ref_prefix"]),
            "catalog-scoped reference requires an explicit development mock prefix",
        )
    return reference


def availability(result, kind, complete=False):
    require(type(result.get("partial")) is bool, f"{kind}: missing boolean partial")
    units = result.get("unavailable_units")
    require(isinstance(units, list) and all(isinstance(x, str) and x for x in units),
            f"{kind}: invalid unavailable_units")
    if result["partial"]:
        require(bool(units), f"{kind}: partial without unavailable units")
        if kind == "checksum":
            require("checksum" in result and result["checksum"] is None, "partial checksum must be null")
        if kind == "analytics":
            require("total" in result and result["total"] is None, "partial analytics must not claim a total")
    else:
        require(not units, f"{kind}: complete response lists unavailable units")
    if kind in ("audit", "recent"):
        require(isinstance(result.get("items"), list), f"{kind}: missing items")
        cursor = result.get("next_cursor")
        require("next_cursor" in result and (cursor is None or isinstance(cursor, str) and cursor),
                f"{kind}: invalid next_cursor")
        if result["partial"]:
            require(cursor is not None, f"{kind}: partial response claims end of dataset")
    if complete and result["partial"]:
        raise DataUnavailable(f"{kind}: response is partial")


def event_key(event, order):
    require(isinstance(event.get("hazard_id"), str) and event["hazard_id"], "invalid hazard_id")
    require(isinstance(event.get("source_ref_id"), str) and event["source_ref_id"], "invalid source_ref_id")
    require(event.get("source") in ("BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI"), "invalid source")
    require(event.get("hazard_type") in ("SEISMIC", "VOLCANIC", "WILDFIRE", "COASTAL"), "invalid hazard type")
    require(event.get("severity") in SEVERITIES, "invalid severity")
    require(isinstance(event.get("attributes"), dict), "invalid attributes")
    return timestamp(event["ingested_at" if order == "audit" else "occurred_at"]), event["hazard_id"]


def export(api, order="audit", page_size=50, max_pages=10000, first_page=None):
    """Reject loops, duplicates, wrong ordering and false terminal partial pages."""
    path = "/internal/audit/events" if order == "audit" else "/internal/hazards"
    token = "AUDIT_TOKEN" if order == "audit" else "AGGREGATOR_TOKEN"
    params = {"limit": page_size}
    if order == "recent":
        params["order"] = "recent"
    items, ids, identities, cursors = [], {}, set(), set()
    updated_versions = 0
    previous = None
    started = time.monotonic()
    last_progress = started
    for page_number in range(1, max_pages + 1):
        page = first_page if page_number == 1 and first_page is not None else api.request(path, token, params)
        availability(page, order, complete=True)
        require(len(page["items"]) <= page_size, "server exceeded page limit")
        for event in page["items"]:
            key = event_key(event, order)
            identity = (event["source"], event["source_ref_id"])
            require(previous is None or (key > previous if order == "audit" else key < previous),
                    f"{order}: global ordering violation")
            previous = key
            hazard_id = event["hazard_id"]
            if hazard_id in ids:
                index = ids[hazard_id]
                old = items[index]
                require(identity == (old["source"], old["source_ref_id"]), "hazard ID changed identity during export")
                require(timestamp(event["ingested_at"]) > timestamp(old["ingested_at"]),
                        "duplicate hazard ID without a newer version across pages")
                items[index] = event
                updated_versions += 1
            else:
                require(identity not in identities, "duplicate source identity across pages")
                ids[hazard_id] = len(items)
                identities.add(identity)
                items.append(event)
        cursor = page["next_cursor"]
        if time.monotonic() - last_progress >= 10 or cursor is None:
            detail = f"page {page_number}" + ("; complete" if cursor is None else "")
            progress(f"{order} export events", len(items), started=started, detail=detail)
            last_progress = time.monotonic()
        if cursor is None:
            if updated_versions:
                output("INFO", "Information", f"{order} export observed {updated_versions} live record updates.")
            return sorted(items, key=lambda event: event_key(event, order), reverse=order == "recent")
        require(cursor not in cursors, "pagination cursor loop")
        require(bool(page["items"]), "complete nonterminal page made no progress")
        cursors.add(cursor)
        params["cursor"] = cursor
    raise ValueError("export exceeded maximum pages")


def verify_checksum(result, events):
    availability(result, "checksum", complete=True)
    require(result.get("algorithm") == "sha256", "unexpected checksum algorithm")
    require(type(result.get("count")) is int and result["count"] == len(events), "checksum count mismatch")
    require(result.get("checksum") == checksum(e["source_ref_id"] for e in events), "checksum/export mismatch")


def preserve(before, after, immutable_refs=()):
    """Require every old identity to survive; only the controlled catalog is immutable.

    Live wildfire aggregates and tsunami warnings can legitimately change their
    payloads. Record those updates rather than equating an update with data loss.
    """
    current = {e["hazard_id"]: e for e in after}
    updates = []
    for event in before:
        hazard_id = event["hazard_id"]
        require(hazard_id in current, f"previously stored event disappeared: {hazard_id}")
        saved = current[hazard_id]
        identity = (event["source"], event["source_ref_id"])
        require((saved["source"], saved["source_ref_id"]) == identity,
                f"previously stored event identity changed: {hazard_id}")
        if saved != event:
            fields = sorted(key for key in event.keys() | saved.keys() if event.get(key) != saved.get(key))
            require(identity not in immutable_refs,
                    f"controlled catalog event changed: {hazard_id}; fields: {', '.join(fields)}")
            updates.append({"hazard_id": hazard_id, "source": event["source"], "changed_fields": fields})
    return updates


def distribution(api, events, redundant=True, poll_interval=2):
    require(bool(events), "cannot prove distribution on an empty dataset")
    nodes, primaries, locations = Counter(), Counter(), []
    started = time.monotonic()
    total = len(events)
    last_progress = started
    progress("replica placement", 0, total, started)
    for checked, event in enumerate(events, start=1):
        event_started = time.monotonic()
        last_wait_report = event_started - 10
        while True:
            result = api.request("/internal/audit/location", "AUDIT_TOKEN", {"hazard_id": event["hazard_id"]})
            require(result.get("hazard_id") == event["hazard_id"], "location returned wrong hazard")
            require(type(result.get("partial")) is bool, "location missing partial flag")
            if not redundant or not result["partial"]:
                break
            live_units = [copy.get("unit") for copy in result.get("locations", [])]
            if time.monotonic() - last_wait_report >= 10:
                progress(
                    "replica placement", checked - 1, total, started,
                    detail=f"waiting {time.monotonic() - event_started:.0f}s for "
                           f"{event['hazard_id']}; live units: {live_units}",
                )
                last_wait_report = time.monotonic()
            time.sleep(poll_interval)
        copies = result.get("locations")
        require(isinstance(copies, list) and copies, "no live copy for event")
        require(all(isinstance(c, dict) and c.get("role") in ("primary", "replica")
                    and isinstance(c.get("node"), str) and c["node"]
                    and isinstance(c.get("unit"), str) and c["unit"] for c in copies), "invalid location")
        require(len({c["unit"] for c in copies}) == len(copies), "duplicate storage unit")
        primary = [c for c in copies if c["role"] == "primary"]
        require(len(primary) == 1, "expected one primary per event")
        hosts = {c["node"] for c in copies}
        require(not redundant or len(hosts) >= 2, "event lacks copies on two distinct nodes")
        nodes.update(hosts)
        primaries.update([primary[0]["node"]])
        locations.append(result)
        if time.monotonic() - last_progress >= 10 or checked == total:
            progress("replica placement", checked, total, started)
            last_progress = time.monotonic()
    require(len(primaries) >= 2, "primary writes/data are concentrated on one node")
    return {"events": len(events), "copies_per_node": dict(nodes), "primary_events_per_node": dict(primaries),
            "redundancy_checked": redundant, "locations": locations}


def send_seismic(run, records):
    """Spread batches over the measurement window so node metrics can refresh."""
    started = time.monotonic()
    batch_size = run.config.batch_size
    batch_count = (len(records) + batch_size - 1) // batch_size
    changed_records = 0
    progress("catalog records submitted", 0, len(records), started)

    for batch_number, offset in enumerate(range(0, len(records), batch_size), start=1):
        batch = records[offset:offset + batch_size]
        result = run.api.request(
            "/internal/ingest/bmkg", "AGGREGATOR_TOKEN",
            body={"seismic_events": batch, "tsunami_warnings": []},
        )
        changed = result.get("changed")
        require(
            type(changed) is int and 0 <= changed <= len(batch),
            "invalid ingest acknowledgement",
        )
        changed_records += changed
        next_batch_at = started + run.config.load_seconds * batch_number / batch_count
        time.sleep(max(0, next_batch_at - time.monotonic()))
        progress(
            "catalog records submitted", min(offset + len(batch), len(records)), len(records), started,
            detail=f"changed {changed_records}",
        )
    return changed_records


def verify_analytics(events, summary, query):
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
        require(isinstance(counts, dict) and set(counts) == set(TYPES)
                and all(type(n) is int and n >= 0 for n in counts.values()), "invalid timeline counts")
        times.append(timestamp(row["at"]))
    require(all(a < b for a, b in zip(times, times[1:])), "timeline must be ordered without duplicates")
    return reconcile(events, summary, timestamp(query["from"]),
                               timestamp(query["to"]), query["interval"])


def analytic_query(events, interval="month", end=None):
    require(bool(events), "cannot reconcile an empty P1 dataset")
    times = [timestamp(e["occurred_at"]) for e in events]
    start = min(times).replace(hour=0, minute=0, second=0, microsecond=0)
    if end:
        end = timestamp(end)
    else:
        last_day = max(times).replace(hour=0, minute=0, second=0, microsecond=0)
        end = last_day + dt.timedelta(days=1)
    require(start < end, "analytics_end must be after the first historical event")
    at, buckets = bucket(start, interval), 0
    while at < end:
        buckets += 1
        require(buckets <= 1000, "analytics range exceeds 1000 buckets; choose a coarser interval")
        at = next_bucket(at, interval)
    return {"from": start.isoformat(), "to": end.isoformat(), "interval": interval}


def reconcile_stable_window(before, after, summary, query):
    """Exact chart comparison over a historical window unchanged across both exports."""
    start, end = timestamp(query["from"]), timestamp(query["to"])
    def selected(events):
        return {event["hazard_id"]: event for event in events
                if start <= timestamp(event["occurred_at"]) < end}
    first, last = selected(before), selected(after)
    if first != last:
        raise ConcurrentDataChange(
            f"historical chart data changed during measurement ({len(first)} → {len(last)} events); "
            "choose an earlier CONFIG.analytics_end if this window still receives updates"
        )
    return verify_analytics(list(last.values()), summary, query)


def verify_reference(run, events):
    if not run.config.reference:
        return
    reference = read_reference(run.config.reference)
    original_bmkg = [
        event for event in events
        if event["source"] == "BMKG" and event["source_ref_id"] not in run.surge_refs
    ]
    if reference.get("scope") == "catalog":
        expected_refs = {record["event_id"] for record in read_catalog(run.config.catalog)}
        require(
            len(expected_refs) == reference["count"] and checksum(expected_refs) == reference["checksum"],
            "configured catalog file disagrees with its development reference",
        )
        original_bmkg = [event for event in original_bmkg if event["source_ref_id"] in expected_refs]
    digest = checksum(event["source_ref_id"] for event in original_bmkg)
    require(
        len(original_bmkg) == reference["count"] and digest == reference["checksum"],
        "BMKG data disagrees with the configured reference (excluding this run's regional surge)",
    )
    run.save("reference.json", reference)
    return {"matched": True, "count": len(original_bmkg), "checksum": digest,
            "scope": reference.get("scope", "source")}


def verify_global_reads(before, after, recent, latest):
    """Verify global order and completeness for records stable across live reads."""
    before_by_id = {event["hazard_id"]: event for event in before}
    after_by_id = {event["hazard_id"]: event for event in after}
    stable = {
        hazard_id: event for hazard_id, event in before_by_id.items()
        if after_by_id.get(hazard_id) == event
    }
    recent_by_id = {event["hazard_id"]: event for event in recent}
    for hazard_id, event in stable.items():
        require(recent_by_id.get(hazard_id) == event,
                f"recent pagination omitted or changed a stable event: {hazard_id}")

    availability(latest, "recent", complete=True)
    require(len(latest["items"]) == 50, "latest-50 returned fewer than 50 events")
    keys = [event_key(event, "recent") for event in latest["items"]]
    require(all(a > b for a, b in zip(keys, keys[1:])), "latest-50 global ordering violation")
    latest_ids = {event["hazard_id"] for event in latest["items"]}
    boundary = keys[-1]
    for hazard_id, event in stable.items():
        if event_key(event, "recent") >= boundary:
            require(hazard_id in latest_ids, f"latest-50 omitted a higher-ranked stable event: {hazard_id}")
    for event in latest["items"] + recent:
        saved = after_by_id.get(event["hazard_id"])
        require(saved is not None, "recent query returned an event absent from the later audit export")
        require((saved["source"], saved["source_ref_id"]) == (event["source"], event["source_ref_id"]),
                "recent/audit event identities disagree")
        if event["hazard_id"] in stable:
            require(event == saved, "recent query changed a stable canonical event")

    unchanged = before_by_id == after_by_id
    if unchanged:
        expected = sorted(before, key=lambda event: event_key(event, "recent"), reverse=True)
        require(recent == expected and latest["items"] == expected[:50],
                "recent pagination/latest-50 disagrees with the unchanged full audit")
    return {
        "global_order_verified": True,
        "recent_events": len(recent),
        "stable_events_compared": len(stable),
        "live_records_added_or_updated": len(after_by_id) - len(stable),
        "exact_whole_dataset_comparison": unchanged,
        "comparison": "unchanged dataset" if unchanged else "stable records across live exports",
    }


def read_checksums(run):
    results = {}
    for source in ("all", "BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI"):
        params = None if source == "all" else {"source": source}
        response = run.api.request("/internal/audit/checksum", "AUDIT_TOKEN", params)
        availability(response, "checksum", complete=True)
        require(type(response.get("count")) is int and response["count"] >= 0, "invalid checksum count")
        require(response.get("algorithm") == "sha256" and isinstance(response.get("checksum"), str)
                and re.fullmatch("[0-9a-f]{64}", response["checksum"]), "invalid checksum response")
        results[source] = response
    return results


def verify_sources(before_sums, after_sums, before_events, after_events):
    """An exact checksum comparison requires identical bookend source identities."""
    results = {}
    growing = []
    for source, first in before_sums.items():
        last = after_sums[source]
        old_rows = [e for e in before_events if source == "all" or e["source"] == source]
        new_rows = [e for e in after_events if source == "all" or e["source"] == source]
        require(first["count"] <= len(old_rows) <= len(new_rows) <= last["count"],
                f"{source}: export counts fall outside the live checksum bounds")
        unchanged = first["count"] == last["count"] and first["checksum"] == last["checksum"]
        if unchanged:
            verify_checksum(first, old_rows)
            verify_checksum(last, new_rows)
        else:
            require(last["count"] > first["count"], f"{source}: checksum changed without source growth")
            growing.append(f"{source}: {first['count']} → {last['count']} (+{last['count'] - first['count']})")
        results[source] = {
            "before": first, "after": last,
            "independent_checksum_verified": unchanged,
            "status": "exact_match" if unchanged else "concurrent_writes_observed",
            "exported_before": len(old_rows), "exported_after": len(new_rows),
        }
    if growing:
        output("INFO", "Feed masih menambah event selama pemeriksaan", growing[0],
               "Angka sebelum dan sesudah diambil pada waktu berbeda, bukan satu snapshot database.",
               "Checksum seluruh source yang bertambah tidak diklaim sama. Checksum katalog referensi "
               "tetap diperiksa tepat; event stabil dan data yang sudah diketahui tetap harus terjaga.",
               *growing[1:])
    return results


def verify_preservation(run, events, expected_writes):
    updates = preserve(run.previous_events, events, run.catalog_identities)
    stored_reports = {event["source_ref_id"] for event in events if event["source"] == "PVMBG"}
    require(expected_writes <= stored_reports,
            "an acknowledged continuity write is missing after recovery")
    return updates


def read_live_view(run):
    """Bracket independent API reads; never claim that live endpoints share a snapshot."""
    attempt = 0
    while True:
        attempt += 1
        output("PROGRESS", f"Live comparison window {attempt}",
               "Audit sebelum → latest-50 beserta seluruh cursor → analytics → Audit sesudah.",
               "Dua ekspor membedakan perubahan feed yang sah dari event yang hilang; "
               "permintaan terpisah tidak dianggap memiliki snapshot database yang sama.")
        expected_writes = set(run.acknowledged_writes)
        sums_before = read_checksums(run)
        before = export(run.api, page_size=run.config.page_size, max_pages=run.config.max_pages)
        require(len(before) >= 51, "need at least 51 events to exercise latest-50 pagination")
        output("CAKUPAN", "Pembacaan global", f"Audit awal: {len(before)} event.",
               "Seluruh halaman recent dibaca dengan limit=50; dataset besar memerlukan lebih banyak request.")
        latest = run.api.request("/internal/hazards", "AGGREGATOR_TOKEN", {"order": "recent", "limit": 50})
        recent = export(run.api, "recent", 50, run.config.max_pages, first_page=latest)
        query = analytic_query(before, run.config.interval, run.config.analytics_end)
        output("PROGRESS", "Analytics comparison range", f"From: {query['from']}", f"To: {query['to']}")
        summary = run.api.request("/internal/analytics", "AGGREGATOR_TOKEN", query)
        after = export(run.api, page_size=run.config.page_size, max_pages=run.config.max_pages)
        sums_after = read_checksums(run)

        preserve(before, after, run.catalog_identities)
        live_updates = verify_preservation(run, after, expected_writes)
        reference = verify_reference(run, after)
        source_checks = verify_sources(sums_before, sums_after, before, after)
        if getattr(run, "mock_polling_frozen", False):
            require(all(source_checks[source]["independent_checksum_verified"]
                        for source in ("BMKG", "PVMBG")),
                    "mock source changed during a frozen checkpoint; check for concurrent direct writes")
        global_reads = verify_global_reads(before, after, recent, latest)
        try:
            comparison = reconcile_stable_window(before, after, summary, query)
        except ConcurrentDataChange as error:
            output("WAIT", "Waiting", f"{error}; retrying comparison window (Ctrl+C to stop)")
            time.sleep(run.config.sample_interval)
            continue
        return {
            "events": after, "checksum": sums_after["all"], "reference": reference,
            "source_checks": source_checks, "global_reads": global_reads,
            "query": query, "analytics": summary, "reconciliation": comparison,
            "live_updates_since_previous_checkpoint": live_updates,
        }


def capture_snapshot(run, name, redundant=True):
    view = read_live_view(run)
    events = view["events"]
    placement = None
    if redundant:
        # Exhaust the controlled dataset rather than querying a constantly growing
        # live feed's location for every chart reconciliation.
        placement_events = []
        for event in events:
            identity = (event["source"], event["source_ref_id"])
            from_catalog = identity in run.catalog_identities
            from_surge = event["source_ref_id"] in run.surge_refs
            from_probe = (event["source"] == "PVMBG"
                          and event["source_ref_id"] in run.acknowledged_writes)
            if from_catalog or from_surge or from_probe:
                placement_events.append(event)
        placement = distribution(run.api, placement_events, poll_interval=run.config.sample_interval)
    topology = cluster_topology(run.config)
    if placement:
        cluster_nodes = {node["name"] for node in topology["nodes"]}
        require(set(placement["copies_per_node"]) <= cluster_nodes,
                "audit locations do not correspond to Kubernetes node identities")
    run.previous_events = events
    run.analytics_query = view["query"]
    run.baseline_analytics = view["analytics"]
    run.save(f"{name}.json", dict(view, placement=placement,
                                placement_scope="controlled_catalog_and_run_writes", topology=topology))
    placement_summary = None if placement is None else {
        key: value for key, value in placement.items() if key != "locations"
    }
    return {"events": len(events), "checksum": view["checksum"]["checksum"],
            "source_checks": {source: result["status"] for source, result in view["source_checks"].items()},
            "global_reads": view["global_reads"], "placement": placement_summary,
            "analytics": view["reconciliation"], "reference": view["reference"],
            "analytics_range": view["query"], "analytics_by_area": view["analytics"]["by_area"]}
