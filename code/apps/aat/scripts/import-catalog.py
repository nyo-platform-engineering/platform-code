#!/usr/bin/env python3
"""Import SeismicEvent JSON/NDJSON through the existing BMKG ingestion contract."""
import argparse
import datetime
import json
import math
from pathlib import Path
import sys

from analytics_http import API, checksum


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
        at = datetime.datetime.fromisoformat(record["occurred_at"].replace("Z", "+00:00"))
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("catalog", type=Path)
    parser.add_argument("--url", default="http://127.0.0.1:8083", help="Aggregator origin (use a local port-forward)")
    parser.add_argument("--env-file", type=Path)
    parser.add_argument("--batch-size", type=int, default=100)
    parser.add_argument("--dry-run", action="store_true", help="validate the entire input without sending requests")
    parser.add_argument("--output", type=Path, help="write a JSON import summary")
    args = parser.parse_args()
    if not 1 <= args.batch_size <= 200:
        parser.error("--batch-size must be 1..200")
    records = read_catalog(args.catalog)
    refs = {record["event_id"] for record in records}
    result = {"records": len(records), "unique_source_ref_ids": len(refs), "catalog_ref_checksum": checksum(refs), "changed": 0, "dry_run": args.dry_run}
    if not args.dry_run:
        api = API(args.url, args.env_file)
        for offset in range(0, len(records), args.batch_size):
            batch = records[offset:offset + args.batch_size]
            response = api.request("/internal/ingest/bmkg", "AGGREGATOR_TOKEN", body={"seismic_events": batch, "tsunami_warnings": []})
            if type(response.get("changed")) is not int or not 0 <= response["changed"] <= len(batch):
                raise ValueError("unexpected ingestion response")
            result["changed"] += response["changed"]
            print(f"Processed {min(offset + len(batch), len(records))}/{len(records)} records", file=sys.stderr)
    if args.output:
        args.output.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
