"""Group 1 — catalog placement, uneven load, and global reads (criteria 1–3)."""
# 1. Muat dataset katalog gempa historis yang diberikan asisten. Tidak ada satu node pun yang menyimpan seluruh data, dan beban penyimpanan maupun penulisan tersebar ke lebih dari satu node. Laporkan sebaran data per node.
# 2. Tunjukkan bagaimana sistem menyikapi ketidakmerataan alami dataset tersebut. Ukur beban tiap node saat dataset dimuat dan saat lonjakan gempa di satu wilayah disimulasikan.
# 3. Sediakan permintaan yang membaca data lintas seluruh lokasi penyimpanan (contoh: "50 event terbaru dari seluruh jenis bahaya", dengan pagination) dan menghasilkan urutan yang benar.
from collections import Counter

from helper.monitor import Monitor
from helper.data import (
    capture_snapshot, checksum, distribution, export, preserve, read_catalog,
    read_live_view, send_seismic,
)
from common import output, require, utc


# 1. Muat dataset katalog gempa historis yang diberikan asisten. Tidak ada satu node pun yang menyimpan seluruh data, dan beban penyimpanan maupun penulisan tersebar ke lebih dari satu node. Laporkan sebaran data per node.
def catalog_distribution(run):
    # Validate the catalog before sending any records to the cluster.
    records = read_catalog(run.config.catalog)
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

    # Identify existing records so a replay is not reported as a fresh load.
    before = export(
        run.api,
        page_size=run.config.page_size,
        max_pages=run.config.max_pages,
    )
    existing_references = {
        event["source_ref_id"] for event in before if event["source"] == "BMKG"
    }
    already_present = existing_references.intersection(catalog_references)

    if already_present:
        output(
            "INFO",
            "Information",
            f"{len(already_present)} catalog events already exist. Replaying the same "
            "records without deleting data; this rerun alone does not prove a fresh catalog load.",
        )

    run.save("catalog-manifest.json", {
        "records": len(records),
        "unique_ids": len(catalog_references),
        "checksum": checksum(catalog_references),
        "by_area": dict(Counter(record["region_name"] for record in records)),
        "already_present": len(already_present),
        "new_catalog_ids": len(catalog_references - already_present),
        "fresh_import": not already_present,
    })

    # Measure reads and node load while the catalog is being imported.
    with Monitor(run, "catalog-load", writes=False):
        changed_records = send_seismic(run, records)

    # Check that old events survived and every catalog ID was stored.
    events = export(
        run.api,
        page_size=run.config.page_size,
        max_pages=run.config.max_pages,
    )
    updates = preserve(before, events, run.catalog_identities)
    run.save("catalog-live-updates.json", updates)
    stored_references = {
        event["source_ref_id"] for event in events if event["source"] == "BMKG"
    }
    require(catalog_references <= stored_references, "imported catalog events missing")
    # Report placement for this catalog, excluding unrelated live events.
    imported = [
        event for event in events
        if event["source"] == "BMKG" and event["source_ref_id"] in catalog_references
    ]
    placement = distribution(
        run.api, imported,
        poll_interval=run.config.sample_interval,
    )
    run.save("catalog-placement.json", placement)

    # Keep a verified baseline for the later surge and recovery cases.
    capture_snapshot(run, "baseline")

    return {
        "records": len(records),
        "primary_events_per_node": placement["primary_events_per_node"],
        "copies_per_node": placement["copies_per_node"],
        "live_events_updated_during_import": len(updates),
        "already_present": len(already_present),
        "changed_records": changed_records,
        "fresh_write_distribution_proven": not already_present,
    }


# 2. Tunjukkan bagaimana sistem menyikapi ketidakmerataan alami dataset tersebut. Ukur beban tiap node saat dataset dimuat dan saat lonjakan gempa di satu wilayah disimulasikan.
def uneven_load(run):
    # Give every synthetic event a new ID, but keep them in the same region.
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

    # Use the same load window as the catalog import so metrics can refresh.
    with Monitor(run, "regional-surge", writes=False):
        send_seismic(run, records)

    # Compare the whole store, then check placement for the surge alone.
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


# 3. Sediakan permintaan yang membaca data lintas seluruh lokasi penyimpanan (contoh: "50 event terbaru dari seluruh jenis bahaya", dengan pagination) dan menghasilkan urutan yang benar.
def global_reads(run):
    # Check all recent-event pages against the surrounding Audit API exports.
    view = read_live_view(run)

    # Carry the verified data forward to the worker-loss tests.
    run.previous_events = view["events"]
    run.save("global-reads.json", view)

    return view["global_reads"]


def run_group(run):
    print("\nGroup 1: data distribution and reads — criteria 1–3", flush=True)

    # 1. Muat dataset katalog gempa historis yang diberikan asisten. Tidak ada satu node pun yang menyimpan seluruh data, dan beban penyimpanan maupun penulisan tersebar ke lebih dari satu node. Laporkan sebaran data per node.
    input("Enter → import the historical catalog and report per-node distribution: ")
    run.stage("catalog-import", catalog_distribution, run)

    # 2. Tunjukkan bagaimana sistem menyikapi ketidakmerataan alami dataset tersebut. Ukur beban tiap node saat dataset dimuat dan saat lonjakan gempa di satu wilayah disimulasikan.
    input("Enter → simulate an earthquake surge in one region and measure its distribution: ")
    run.stage("regional-surge", uneven_load, run)

    # 3. Sediakan permintaan yang membaca data lintas seluruh lokasi penyimpanan (contoh: "50 event terbaru dari seluruh jenis bahaya", dengan pagination) dan menghasilkan urutan yang benar.
    input("Enter → verify globally ordered latest-50 reads and pagination: ")
    run.stage("global-reads", global_reads, run)
