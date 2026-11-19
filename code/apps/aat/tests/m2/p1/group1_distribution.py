"""Group 1 — catalog placement, uneven load, and global reads (criteria 1–3)."""
from collections import Counter
import time

from helper import output, analytics, catalog, checksum, progress, require, utc


def availability(result, kind, complete=False):
    require(type(result.get("partial")) is bool, f"{kind}: missing boolean partial")
    units = result.get("unavailable_units")
    require(isinstance(units, list) and all(isinstance(x, str) and x for x in units),
            f"{kind}: invalid unavailable_units")
    if result["partial"]:
        require(not complete, f"{kind}: response is partial")
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


def event_key(event, order):
    require(isinstance(event.get("hazard_id"), str) and event["hazard_id"], "invalid hazard_id")
    require(isinstance(event.get("source_ref_id"), str) and event["source_ref_id"], "invalid source_ref_id")
    require(event.get("source") in ("BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI"), "invalid source")
    require(event.get("hazard_type") in ("SEISMIC", "VOLCANIC", "WILDFIRE", "COASTAL"), "invalid hazard type")
    require(event.get("severity") in analytics.SEVERITIES, "invalid severity")
    require(isinstance(event.get("attributes"), dict), "invalid attributes")
    return analytics.timestamp(event["ingested_at" if order == "audit" else "occurred_at"]), event["hazard_id"]


def export(api, order="audit", page_size=50, max_pages=10000):
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
        page = api.request(path, token, params)
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
                require(analytics.timestamp(event["ingested_at"]) > analytics.timestamp(old["ingested_at"]),
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
    require(all(count < len(events) for count in nodes.values()), "one node holds the whole dataset")
    return {"events": len(events), "copies_per_node": dict(nodes), "primary_events_per_node": dict(primaries),
            "redundancy_checked": redundant, "locations": locations}


def ingest_catalog(run):
    from group2_recovery import Monitor

    records = catalog.read_catalog(run.config.catalog)
    unique_records = {}
    for record in records:
        reference = record["event_id"]
        require(
            reference not in unique_records or unique_records[reference] == record,
            "catalog contains conflicting records for one event_id",
        )
        unique_records[reference] = record
    catalog_references = set(unique_records)
    require(len(catalog_references) >= 51, "catalog needs at least 51 unique events")

    before = export(run.api, page_size=run.config.page_size, max_pages=run.config.max_pages)
    existing_references = {
        event["source_ref_id"] for event in before if event["source"] == "BMKG"
    }
    already_present = existing_references.intersection(catalog_references)
    if already_present:
        output("INFO", "Information", f"{len(already_present)} catalog events already exist. Replaying the same "
            "records without deleting data; this rerun alone does not prove a fresh catalog load.")
    run.save("catalog-manifest.json", {
        "records": len(records),
        "unique_ids": len(catalog_references),
        "checksum": checksum(catalog_references),
        "by_area": dict(Counter(record["region_name"] for record in records)),
        "already_present": len(already_present),
        "new_catalog_ids": len(catalog_references - already_present),
        "fresh_import": not already_present,
    })

    with Monitor(run, "catalog-load", writes=False):
        changed_records = send_seismic(run, records)

    events = export(run.api, page_size=run.config.page_size, max_pages=run.config.max_pages)
    updates = preserve(before, events, run.catalog_identities)
    run.save("catalog-live-updates.json", updates)
    stored_references = {
        event["source_ref_id"] for event in events if event["source"] == "BMKG"
    }
    require(catalog_references <= stored_references, "imported catalog events missing")
    imported = [
        event for event in events
        if event["source"] == "BMKG" and event["source_ref_id"] in catalog_references
    ]
    placement = distribution(
        run.api, imported,
        poll_interval=run.config.sample_interval,
    )
    run.save("catalog-placement.json", placement)
    return {
        "records": len(records),
        "primary_events_per_node": placement["primary_events_per_node"],
        "copies_per_node": placement["copies_per_node"],
        "live_events_updated_during_import": len(updates),
        "already_present": len(already_present),
        "changed_records": changed_records,
        "fresh_write_distribution_proven": not already_present,
    }


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


def create_regional_events(run):
    """Vary event IDs while keeping all earthquake locations in the same region."""
    records = []
    for number in range(run.config.surge_events):
        reference = f"{run.run_id}-regional-{number}"
        run.surge_refs.add(reference)
        records.append({
            "event_id": reference,
            "region_name": "M2 P1 regional surge: Jawa",
            "occurred_at": utc(),
            "epicenter_lat": -7.5,
            "epicenter_lon": 110.4,
            "magnitude": 5.5,
            "depth_km": 10,
            "potential_tsunami": False,
        })
    return records


def surge(run):
    from group2_recovery import Monitor, capture_snapshot

    records = create_regional_events(run)
    with Monitor(run, "regional-surge", writes=False):
        send_seismic(run, records)

    result = capture_snapshot(run, "after-surge")
    surged = [
        event for event in run.previous_events
        if event["source"] == "BMKG" and event["source_ref_id"] in run.surge_refs
    ]
    require(len(surged) == len(records), "regional surge lost events")
    placement = distribution(
        run.api, surged,
        poll_interval=run.config.sample_interval,
    )
    result["surge_distribution"] = {
        key: value for key, value in placement.items() if key != "locations"
    }
    return result


def run_group(run):
    from group2_recovery import capture_snapshot

    print("\nGroup 1: data distribution and reads — criteria 1–3", flush=True)
    input("Enter → import the historical catalog and measure per-node load: ")
    run.stage("catalog-import", ingest_catalog, run)
    input("Enter → verify data distribution, global pagination, and baseline checksum: ")
    run.stage("baseline", capture_snapshot, run, "baseline")
    input("Enter → simulate an earthquake surge in one region and measure its distribution: ")
    run.stage("regional-surge", surge, run)
