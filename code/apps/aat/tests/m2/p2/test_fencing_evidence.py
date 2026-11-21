"""Storage evidence rejects competing and stale writers."""
import copy
import unittest

import helper
from fencing_evidence import freeze_evidence, storage_evidence


def commit(source="BMKG", epoch=1, uid="a", second=1, observed=10):
    return {"observed": observed, "initial": False, "pod_uid": uid, "node": "worker",
            "log": {"msg": "poll storage committed", "source": source, "epoch": epoch,
                    "owner": f"owner-{epoch}", "backend_pid": 100 + epoch, "operation": "raw_capture",
                    "transaction_id": str(1000 + second),
                    "db_observed_at": f"2026-11-21T12:00:{second:02d}.123456789Z"}}


class StorageEvidenceTests(unittest.TestCase):
    def baseline(self):
        return [commit(), commit("PVMBG", uid="p", second=2)]

    def test_worker_clock_is_not_commit_ordering_authority(self):
        records = self.baseline()
        records[0]["log"]["time"] = "2026-11-21T12:00:45Z"
        records[1]["log"]["time"] = "2026-11-21T11:59:30Z"
        self.assertEqual(storage_evidence(records, 0, 20)["commits_per_source"], {"BMKG": 1, "PVMBG": 1})

    def test_two_instances_cannot_share_one_storage_epoch(self):
        records = self.baseline() + [commit(uid="other", second=3)]
        with self.assertRaisesRegex(ValueError, "different sessions"):
            storage_evidence(records, 0, 20)

    def test_old_epoch_cannot_store_after_new_epoch(self):
        records = self.baseline() + [commit(epoch=2, uid="b", second=3), commit(second=4)]
        with self.assertRaisesRegex(ValueError, "old epoch"):
            storage_evidence(records, 0, 20)

    def test_receipts_need_transaction_metadata(self):
        records = self.baseline()
        del records[0]["log"]["transaction_id"]
        with self.assertRaisesRegex(ValueError, "identity"):
            storage_evidence(records, 0, 20)

    def freeze_records(self):
        old = commit(uid="old", second=1, observed=210)
        successor = commit(epoch=2, uid="new", second=10, observed=150)
        rejected = {"observed": 220, "pod_uid": "old", "log": {
            "msg": "poll storage rejected", "source": "BMKG", "epoch": 1, "reason": "session_unavailable"}}
        acquired = {"observed": 230, "pod_uid": "old", "log": {
            "msg": "poll ownership acquired", "source": "BMKG", "epoch": 3, "owner": "owner-3", "backend_pid": 103}}
        renewed = commit(epoch=3, uid="old", second=20, observed=240)
        return [old, successor, rejected, acquired, renewed]

    def verify_freeze(self, records):
        return freeze_evidence(records, {"pod_uid": "old"}, "BMKG", 1, 2, 200)

    def test_buffered_pre_freeze_receipt_is_not_a_stale_write(self):
        proof = self.verify_freeze(self.freeze_records())
        self.assertEqual(len(proof["old_session_rejections"]), 1)
        self.assertEqual(len(proof["commits_after_fresh_acquisition"]), 1)

    def test_resume_requires_direct_rejection_evidence(self):
        records = self.freeze_records()
        del records[2]
        with self.assertRaisesRegex(ValueError, "rejection evidence"):
            self.verify_freeze(records)

    def test_old_session_write_after_takeover_is_rejected(self):
        records = self.freeze_records() + [commit(uid="old", second=15, observed=225)]
        with self.assertRaisesRegex(ValueError, "old session stored"):
            self.verify_freeze(records)

    def test_renewed_write_requires_matching_acquisition(self):
        records = self.freeze_records()
        records[3] = copy.deepcopy(records[3])
        records[3]["log"]["backend_pid"] = 999
        with self.assertRaisesRegex(ValueError, "matching fresh"):
            self.verify_freeze(records)

    def test_matching_acquisition_must_precede_the_write(self):
        records = self.freeze_records()
        records[3], records[4] = records[4], records[3]
        with self.assertRaisesRegex(ValueError, "matching fresh"):
            self.verify_freeze(records)


if __name__ == "__main__":
    unittest.main()
