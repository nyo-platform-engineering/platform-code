"""Offline checks for catalog validation and independent audit reconciliation."""
import copy
import datetime as dt
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS))
from analytics_http import checksum


def module(name, file):
    spec = importlib.util.spec_from_file_location(name, SCRIPTS / file)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


catalog = module("catalog_import", "import-catalog.py")
verify = module("verify_analytics", "verify-analytics.py")


class CatalogTests(unittest.TestCase):
    def setUp(self):
        self.record = dict(event_id="eq-1", region_name="Jawa", occurred_at="2026-11-18T00:00:00Z", epicenter_lat=-6, epicenter_lon=106, magnitude=5, depth_km=10, potential_tsunami=False)

    def read(self, records, ndjson=False):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "catalog.json"
            data = "\n".join(json.dumps(r) for r in records) if ndjson else json.dumps(records)
            path.write_text(data)
            return catalog.read_catalog(path)

    def test_json_and_ndjson_preserve_records_and_duplicate_ids(self):
        for ndjson in (False, True):
            self.assertEqual(self.read([self.record, self.record], ndjson), [self.record, self.record])

    def test_reject_invalid_records_before_import(self):
        for key, value in (("event_id", ""), ("potential_tsunami", "false"), ("magnitude", 10**400), ("epicenter_lat", True), ("epicenter_lon", 181), ("occurred_at", "2026-11-18T00:00:00")):
            broken = dict(self.record, **{key: value})
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.read([self.record, broken])

    def test_checksum_uses_byte_order_and_no_trailing_newline(self):
        self.assertEqual(checksum(["é", "z", "A"]), hashlib.sha256("A\nz\né".encode()).hexdigest())


class VerificationTests(unittest.TestCase):
    def setUp(self):
        self.start = verify.timestamp("2026-11-18T00:00:00Z")
        self.end = self.start + dt.timedelta(hours=2)
        self.events = [dict(hazard_type="SEISMIC", severity=severity, area_name="Jawa", occurred_at=at, attributes=attrs) for at, severity, attrs in [
            ("2026-11-18T07:00:00+07:00", "NORMAL", {"potential_tsunami": True}),
            ("2026-11-18T01:00:00Z", "AWAS", {"tsunami_warnings": [{}]}),
        ]]
        counts = {key: 0 for key in verify.TYPES}
        first = dict(counts, SEISMIC=1)
        second = dict(first, TSUNAMI=1)
        self.summary = dict(partial=False, total=2, by_type=[dict(key=k, count=v) for k, v in dict(counts, SEISMIC=2, TSUNAMI=1).items()], by_severity=[dict(key=k, count=int(k in ("NORMAL", "AWAS"))) for k in verify.SEVERITIES], by_area=[dict(key="Jawa", count=2)], timeline=[dict(at="2026-11-18T00:00:00Z", counts=first), dict(at="2026-11-18T01:00:00Z", counts=second)])

    def test_tsunami_subset_and_timezone_match_audit(self):
        result = verify.reconcile(self.events, self.summary, self.start, self.end, "hour")
        self.assertTrue(result["matched"])
        self.assertEqual(result["selected_events"], 2)
        self.assertEqual(result["by_type"]["TSUNAMI"], 1)

    def test_mismatch_and_partial_cannot_pass(self):
        broken = copy.deepcopy(self.summary)
        broken["by_area"][0]["count"] = 3
        with self.assertRaises(ValueError):
            verify.reconcile(self.events, broken, self.start, self.end, "hour")
        broken["partial"] = True
        with self.assertRaises(RuntimeError):
            verify.reconcile(self.events, broken, self.start, self.end, "hour")

    def test_end_boundary_is_exclusive(self):
        extra = dict(self.events[0], occurred_at="2026-11-18T02:00:00Z")
        result = verify.reconcile(self.events + [extra], self.summary, self.start, self.end, "hour")
        self.assertEqual(result["selected_events"], 2)

class CalendarTests(unittest.TestCase):
    def test_months_follow_calendar_in_leap_year(self):
        at = verify.timestamp("2024-02-29T10:00:00Z")
        start = verify.bucket(at, "month")
        self.assertEqual(start, verify.timestamp("2024-02-01T00:00:00Z"))
        self.assertEqual(verify.next_bucket(start, "month"), verify.timestamp("2024-03-01T00:00:00Z"))


if __name__ == "__main__":
    unittest.main()
