# Problem 3: schema changes, durable failures and field reports

From `code/infrastructure`:

```bash
task m2:p3
```

Edit `CONFIG` in [test-problem-3.py](test-problem-3.py). The default runs all three
groups with ten field reports. It needs Python 3.9+, curl, kubectl, the running
Kubernetes lab, `.state/kubeconfig`, and `.env.runtime` with Aggregator, PVMBG,
Audit and analyst/responder/public credentials (the Media account is `public`). Use current application images.
Both the VM cluster and k3d can run these scenarios; the k3d result is development
evidence. This suite does not run k6 or require 10,000 VUs.

Use an isolated cluster. Do not run P1/P2/P4/P5 concurrently. Read each Enter prompt.
The script temporarily pauses Argo reconciliation, changes PVMBG schema and
Aggregator environment settings, adds a small upstream fixture, briefly locks one
synthetic hazard row, and force-deletes all Aggregator pods in group 3. It does
not erase node disks or delete accepted reports. The fixture uses Python from the
existing Citus image, avoiding a new test image build.

| Group | Criteria | Automated checks |
| --- | --- | --- |
| [1: schema and access](group1_schema.py) | 1, 2, 5 | Real mock `original`/`additive` accepted; `rename_field`, `type_change`, `missing_required` parked under strict policy; raw, source, time and reason retained; parked attempt count stable; only public analyst can inspect/replay failed messages |
| [2: isolation and replay](group2_recovery.py) | 2, 3, 4 | Truncated JSON, depth >64, invalid UTF-8 and a record >2 MiB parked with original bytes; healthy PVMBG and BMKG continue; no application pod replacement/restart; explicit v2 policy and replay recover selected schema failures; row lock forces SQL timeout with automatic retry; later same-key version stays blocked; final state matches the later version and has one canonical row |
| [3: field reports](group3_reports.py) | 6 | Public responder receives bounded 202 while processing is disabled; same-key concurrent duplicates return the same receipt; conflicting content returns 409; all possible receiver pods are replaced; receipts remain pending and durable; resume creates one result per accepted report and preserves processed timestamps on duplicate retry |

## Declared schema policy

Strict mode accepts original fields and optional `confidence_level`; it parks the
three incompatible mock changes. The explicit `PVMBG_SCHEMA_POLICY=v2` change
accepts `level` as an alias, canonical integer strings for the count, and omission
of the count. Omitted counts remain absent; the mapper does not invent a zero.
Conflicting aliases, invalid numeric strings, and missing fields other than this
explicitly optional count still fail. Group 2 uses this existing policy switch as
the schema adjustment, then replays the actual previously captured records.

Raw details/replay are reachable publicly only through analyst Ops. The internal
Aggregator credential is trusted service authentication. The separately authorized
Audit credential may list failed-message metadata but has no raw-detail/replay
route. The test verifies that application/user tokens do not work as Audit tokens.

## Evidence and limits

Each run writes evidence under `tests/m2/p3/evidence/m2-p3-*/` (ignored by Git):
`result.json`, `console.log` (terminal messages, stderr, progress, Enter prompts and cleanup/summary), bounded HTTP metadata,
original poison/schema inbox details, retry/ordering observations, and accepted
receipts before receiver deletion. SQL is used for independent canonical/result
counts; credentials are read inside the database pod and never put in argv or
evidence. The HTTP helper disables redirects and proxies and imposes a total
deadline. The 2,000 ms acceptance budget is a local test choice, not a number
specified by the PDF. Adjust it explicitly for the demo environment.

Automated stages and cleanup emit `[WAIT]` every five seconds, with the active
phase, elapsed time, timeout budget and last observation (Ready counts, inbox
state/attempts, or pending/processed report counts). The same trail is saved to
`progress.jsonl`. Messages pause while waiting for Enter. A heartbeat means the
runner is waiting; it does not mean the condition has advanced or passed.

The poison test exercises bytes within the 8 MiB durable capture bound. Its
oversized record exceeds the 2 MiB processing bound; it does not claim unlimited
payload capture. Permanently malformed bytes stay parked: changing a schema
mapper cannot repair truncated JSON or invalid UTF-8. Replay recovery claims
cover the selected incompatible schema records, not those poison envelopes or
unrelated failures that already existed before the run.

Ordering evidence covers the forced predecessor failure and final version of a
controlled key. This is not a complete linearizability proof. Field-report checks
cover durable database processing, not exactly-once delivery to an external
notification provider. Reports, official Tester Agent evidence and recorded demo
remain separate; the script never marks the whole milestone certified.

The transient fault locks the physical row on its primary Citus worker, not a
coordinator SELECT whose distributed lock lifetime may differ. The dedicated
session has a 90-second safety expiry and is terminated in cleanup. Retry
observation allows 30 seconds for backoff, polling and bounded SQL failures;
an increased attempt count is still required. Intermediate state is saved even
if the retry check fails.

Finally, the script restores the original Aggregator env overrides, observed
PVMBG schema mode and Argo annotations, and removes the temporary fixture and
port-forwards. It retains synthetic database messages/results for inspection.
On SIGKILL or host failure, cleanup cannot run; inspect `cleanup_errors` and
restore the settings manually before the next test. Evidence and personal notes
must not be staged or committed.
