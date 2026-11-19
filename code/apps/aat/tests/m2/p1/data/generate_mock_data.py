#!/usr/bin/env python3
"""Reproduce the timestamp-based BMKG/PVMBG feeds without running their servers.

Rules are ported from bmkg/internal/mock/model/events.go and
pvmbg/internal/mock/model/reports.go. Run this file to regenerate the JSON data
beside it. This does not connect to the cluster or import any records.
"""
from collections import Counter
import datetime as dt
import hashlib
import json
from pathlib import Path


# Same settings as the services' START_TIME and EVENT_INTERVAL_SECONDS.
# A historical start avoids overlapping the running mocks' current events.
START_TIME = "2020-01-01T00:00:00Z"
EVENT_INTERVAL_SECONDS = 10
RECORD_COUNT = 300
PVMBG_SCHEMA = "original"

OUTPUT_DIR = Path(__file__).resolve().parent
EPOCH = dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc)
GO_ZERO_TO_UNIX_SECONDS = 62135596800


def timestamp(value):
    return value.isoformat().replace("+00:00", "Z")


def unix_nanos(value):
    # Integer arithmetic preserves the timestamp-derived IDs without float rounding.
    delta = value - EPOCH
    return ((delta.days * 86400 + delta.seconds) * 1_000_000 + delta.microseconds) * 1000


def feed_times(start, interval_seconds, count):
    """Match NewFeed's Truncate(interval) minus 19 intervals, then advance."""
    if start.tzinfo is None or start < EPOCH:
        raise ValueError("use a timezone-aware START_TIME on or after 1970-01-01")
    if type(interval_seconds) is not int or not 1 <= interval_seconds <= 10:
        raise ValueError("EVENT_INTERVAL_SECONDS must be 1..10, like the mock servers")
    if type(count) is not int or count < 20:
        raise ValueError("RECORD_COUNT must include at least the initial 20 records")

    # Go Time.Truncate aligns against year 1, including for seven-second intervals.
    absolute_seconds = unix_nanos(start) // 1_000_000_000 + GO_ZERO_TO_UNIX_SECONDS
    aligned_seconds = absolute_seconds // interval_seconds * interval_seconds
    first_seconds = aligned_seconds - GO_ZERO_TO_UNIX_SECONDS - 19 * interval_seconds
    for index in range(count):
        yield EPOCH + dt.timedelta(seconds=first_seconds + index * interval_seconds)


def seismic_event(occurred, interval_seconds):
    nanos = unix_nanos(occurred)
    slot = nanos // (interval_seconds * 1_000_000_000)
    return {
        "event_id": f"bmkg-{nanos}",
        "magnitude": [7.1, 4.2, 5.0, 6.5][slot % 4],
        "depth_km": 10 + slot % 30,
        "epicenter_lat": -8,
        "epicenter_lon": 110,
        "region_name": "Pesisir Selatan Jawa",
        "occurred_at": timestamp(occurred),
        "potential_tsunami": slot % 4 == 0,
    }


def tsunami_warning(occurred, interval_seconds):
    slot = unix_nanos(occurred) // (interval_seconds * 1_000_000_000)
    if slot % 4 != 0:
        return None
    event_id = f"bmkg-{unix_nanos(occurred)}"
    return {
        "warning_id": "warning-" + event_id,
        "related_event_id": event_id,
        "threat_level": ["Waspada", "Siaga", "Awas"][(slot // 4) % 3],
        "affected_zones": ["Pesisir Selatan Jawa"],
        "estimated_arrival": timestamp(occurred + dt.timedelta(minutes=30)),
    }


def volcanic_report(reported, interval_seconds, schema="original"):
    slot = unix_nanos(reported) // (interval_seconds * 1_000_000_000)
    report = {
        "report_id": f"pvmbg-{unix_nanos(reported)}",
        "volcano_id": ["MERAPI", "SEMERU", "ANAK_KRAKATAU"][slot % 3],
        "alert_level": ["Normal", "Waspada", "Siaga", "Awas"][slot % 4],
        "eruption_count_24h": slot % 12,
        "ash_column_height_m": (slot % 8) * 100,
        "reported_at": timestamp(reported),
    }
    if schema == "additive":
        report["confidence_level"] = 0.85
    elif schema == "rename_field":
        report["level"] = report.pop("alert_level")
    elif schema == "type_change":
        report["eruption_count_24h"] = str(report["eruption_count_24h"])
    elif schema == "missing_required":
        del report["eruption_count_24h"]
    elif schema != "original":
        raise ValueError("unknown PVMBG_SCHEMA")
    return report


def write_ndjson(path, records):
    with path.open("w") as stream:
        for record in records:
            stream.write(json.dumps(record, separators=(",", ":"), allow_nan=False) + "\n")


def main():
    start = dt.datetime.fromisoformat(START_TIME.replace("Z", "+00:00"))
    times = list(feed_times(start, EVENT_INTERVAL_SECONDS, RECORD_COUNT))
    earthquakes = [seismic_event(at, EVENT_INTERVAL_SECONDS) for at in times]
    warnings = [warning for at in times if (warning := tsunami_warning(at, EVENT_INTERVAL_SECONDS))]
    reports = [volcanic_report(at, EVENT_INTERVAL_SECONDS, PVMBG_SCHEMA) for at in times]

    references = sorted((row["event_id"] for row in earthquakes), key=lambda value: value.encode("utf-8"))
    reference = {
        "source": "BMKG",
        "count": len(earthquakes),
        "checksum": hashlib.sha256("\n".join(references).encode("utf-8")).hexdigest(),
        "dataset_origin": "development_mock",
        "official": False,
        "scope": "catalog",
        "source_ref_prefix": "bmkg-",
        "based_on": ["bmkg/internal/mock/model/events.go", "pvmbg/internal/mock/model/reports.go"],
        "description": "Exact mock feed rules with a fixed historical START_TIME; no invented regions or ID prefixes.",
        "generator": {"start_time": START_TIME, "event_interval_seconds": EVENT_INTERVAL_SECONDS,
                      "record_count": RECORD_COUNT, "pvmbg_schema": PVMBG_SCHEMA},
        "by_area": dict(Counter(row["region_name"] for row in earthquakes)),
        "first_occurred_at": earthquakes[0]["occurred_at"],
        "last_occurred_at": earthquakes[-1]["occurred_at"],
        "tsunami_warning_count": len(warnings),
        "volcanic_report_count": len(reports),
    }
    write_ndjson(OUTPUT_DIR / "mock-catalog.ndjson", earthquakes)
    write_ndjson(OUTPUT_DIR / "mock-tsunami-warnings.ndjson", warnings)
    write_ndjson(OUTPUT_DIR / "mock-volcanic-reports.ndjson", reports)
    (OUTPUT_DIR / "mock-reference.json").write_text(json.dumps(reference, indent=2) + "\n")
    print(f"Generated {len(earthquakes)} earthquakes, {len(warnings)} tsunami warnings, {len(reports)} volcanic reports.")
    print(f"Dataset: development_mock. Earthquake checksum: {reference['checksum']}")


if __name__ == "__main__":
    main()
