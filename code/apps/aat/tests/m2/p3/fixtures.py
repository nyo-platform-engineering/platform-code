"""P3 payloads and expected inbox IDs."""
import copy
import hashlib
import json


def encode(value):
    return json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()


def message_id(raw, kind="record"):
    return hashlib.sha256(b"PVMBG\0volcanic_reports\0" + kind.encode() + b"\0" + raw).hexdigest()


def report(ref, severity="Waspada", count=2):
    return {"report_id": ref, "volcano_id": "MERAPI", "alert_level": severity,
            "eruption_count_24h": count, "ash_column_height_m": 100,
            "reported_at": "2026-01-01T00:00:00Z"}


def schema_mode(row):
    if "level" in row and "alert_level" not in row:
        return "rename_field"
    if "eruption_count_24h" not in row:
        return "missing_required"
    if isinstance(row["eruption_count_24h"], str):
        return "type_change"
    return "additive" if "confidence_level" in row else "original"


def incompatible_rows(prefix):
    first = report(prefix + "-ordered", "Siaga", 3)
    first["level"] = first.pop("alert_level")
    second = report(prefix + "-ordered", "Awas", 4)
    second["eruption_count_24h"] = "4"
    missing = report(prefix + "-missing")
    del missing["eruption_count_24h"]
    return [first, second, missing]


def poison_cases(prefix):
    oversized = report(prefix + "-oversized")
    oversized["padding"] = "x" * (2 << 20)
    raw = encode(oversized)
    return {
        "truncated": (b'[{"report_id":', "envelope", "invalid_json"),
        "deep": (b"[" * 66 + b"0" + b"]" * 66, "envelope", "nesting_too_deep"),
        "utf8": (b'[{"invalid":"\xff"}]', "envelope", "invalid_utf8"),
        "oversized": (raw, "record", "payload_too_large"),
    }


def changed_report(body):
    changed = copy.deepcopy(body)
    changed["description"] += " changed"
    return changed
