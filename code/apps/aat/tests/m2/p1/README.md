# Milestone 2 — Problem 1

## Three test groups

Start with [test-problem-1.py](test-problem-1.py): its `main()` runs these groups
in order, matching the completion criteria:

| Group | Criteria | Main file |
| --- | --- | --- |
| 1. Data distribution and reads | 1–3: catalog import, placement, uneven regional load, globally ordered pagination | [group1_distribution.py](group1_distribution.py) |
| 2. Failure and recovery | 4–6: worker/data loss, empty-disk rejoin, restored redundancy and another loss | [group2_recovery.py](group2_recovery.py) |
| 3. Analytics dashboard | 7 and its correctness requirements: five categories, severity, area, time buckets and honest partial results | [group3_analytics.py](group3_analytics.py) |

The groups share one run so each recovery check knows which events and
acknowledged writes must survive. Group 3's API comparison also runs at each
checkpoint, and its availability checks run during Group 2's disruptions.
Browser charts and the visible partial/stale banner still require recorded
manual evidence.

The folder contains five Python files: the entry point, the three groups above,
and [helper.py](helper.py). All configuration lives at the top of `test-problem-1.py`. The helper handles preflight checks,
Kubernetes access, HTTP evidence, and shared run state. Group-specific validation,
monitoring, and recovery checkpoints live with the corresponding groups.

## Run the suite

Run from `code/infrastructure`:

```sh
task m2:p1
```

This runs the full guided test, like M1's `test-problem-1.py`. There are no
command-line options. `CONFIG` in [test-problem-1.py](test-problem-1.py) already
points to the bundled development catalog and checksum reference.

### Bundled development dataset

[data/generate_mock_data.py](data/generate_mock_data.py) reproduces the actual
Go model rules in `bmkg/internal/mock/model/events.go` and
`pvmbg/internal/mock/model/reports.go`. Keep this script to regenerate the files;
from `code/infrastructure`, run:

```sh
python3 ../apps/aat/tests/m2/p1/data/generate_mock_data.py
```

The settings are at the top of the generator: `START_TIME`,
`EVENT_INTERVAL_SECONDS`, `WINDOWS`, `VOLCANIC_REPORTS_PER_WINDOW`, and
`PVMBG_SCHEMA`. The defaults sample the mock models at three historical starts
in January–March 2018, with 200/60/40 earthquakes per month. They produce:

- `mock-catalog.ndjson`: 300 historical BMKG earthquake records.
- `mock-tsunami-warnings.ndjson`: 75 warnings correlated with those earthquakes.
- `mock-volcanic-reports.ndjson`: 60 PVMBG reports (20 per window).
- `mock-reference.json`: the earthquake ID checksum, counts, and generation settings.

The generator includes `NewFeed`'s 20 initial seed records, interval alignment,
Unix-nanosecond IDs, timestamp-slot formulas, and PVMBG schema transformations.
A fixed historical start avoids overlapping today's live mock records. BMKG's
actual rule places **every earthquake in Pesisir Selatan Jawa**; the previous
invented 80/10/5/5 regional distribution has been removed. PVMBG rotates through
MERAPI, SEMERU, and ANAK_KRAKATAU. Generated Python records were compared with the
real Go model output, including seven- and ten-second intervals and every schema
mode.

P1 imports the earthquake catalog **together with its tsunami warnings**, then the
60 volcanic reports, within the same measurement window. Warnings accompany
their related earthquakes in the same batch so a later warning import cannot
change an already-established controlled checkpoint. A warning enriches its
seismic HazardEvent; it does not create an extra canonical event.

The 360 canonical records exercise SEISMIC, TSUNAMI, and VOLCANIC analytics,
all four severity levels, four areas, and three populated monthly buckets. The
runner checks that those categories/severities/areas are actually exercised.
Wildfire and coastal data still come from their real integrations; these mock
services do not generate them. All five analytic categories are reconciled with
Audit API, including zeros. Browser behavior still needs demonstration.

The new 2018 IDs avoid rewriting the previous 2020 development catalog, which
remains in the store. The generated development reference names its two companion
files; a reference without `companion_files` imports only its configured catalog.
Switching to official inputs does not automatically mix in these companion files.

The development reference checks exactly the BMKG IDs in the configured catalog,
not every ID beginning with `bmkg-`. This keeps current live mock records separate
from the historical catalog. All-source API checks still cover the full store.
Every existing identity must survive; controlled catalog contents must remain
unchanged. Updates to live records are recorded separately, including in
`catalog-live-updates.json`, rather than treated automatically as lost data.
The APIs use READ COMMITTED and do not share a snapshot across requests. Each
checkpoint therefore brackets recent-page and analytics requests with two full
Audit API exports and source checksums. Global order is checked for every returned
page. Exact cross-endpoint comparisons use records unchanged between exports;
known catalog contents and all previously observed identities remain protected.
A whole-source checksum is independently compared only when its count and digest
are unchanged at both ends. Growing sources are recorded as
`concurrent_writes_observed`, not claimed to have passed an exact snapshot checksum.
The configured catalog/reference checksum remains an exact check on every run.

The reference and `result.json` explicitly label this `development_mock`. It is
useful development evidence and does not fulfill the official assistant
catalog/checksum requirement. Regeneration replaces fixture files on disk but
never changes cluster data. A rerun replays existing catalog records idempotently
without deleting them. The manifest records existing/new IDs, and the result marks
`fresh_write_distribution_proven: false` when records were already present; a
replay alone is not proof of a fresh load. Retain the first-load HTTP/metrics
evidence or use a separately prepared fresh dataset for that proof.

When official resources arrive, change the two paths in `CONFIG`, for example:

```python
CONFIG = TestConfig(
    catalog=Path("/absolute/path/catalog.ndjson"),
    reference=Path("/absolute/path/assistant-reference.json"),
)
```

Their input format must be checked before using the existing BMKG importer.

The defaults in `TestConfig` select workers `aat-worker-1` and `aat-worker-2`,
two explicit 50-event cursor pages, 1,000-record bulk export pages, a 100-event regional surge over at least
20 seconds, and
calendar-month analytics buckets. `analytics_end = None` ends the exact chart
comparison after the catalog's last UTC day. This avoids treating active source
backfill as a chart error. All sources in that date range are counted, without a
source filter; records outside the displayed range are not part of that chart
comparison. Set an explicit RFC3339 `analytics_end` to verify a different range.
If the selected window changes during measurement, it is retried rather than
claimed to match. The actual range is printed and saved for the browser demo.
With companion fixtures enabled, keep the full fixture range so tsunami/volcanic
and severity coverage remain meaningful.
Connection paths, sampling intervals, per-request bounds, and evidence destination
are declared in the same block. These are test settings; the PDF does not prescribe
a P1 load size.

The script waits for **Enter** between catalog import, baseline checks, regional
surge, each recovery action, and the final dashboard comparison. File paths and
worker names come from `CONFIG`; the prompts only advance the test steps.

Worker loss and rejoin actions are automatic by default. At a loss prompt,
**Enter starts actual AAT store-data erasure** on the selected local Vagrant VM:

1. Resolve that worker's Citus PVCs and verify their local PV identities, paths,
   node affinity, and healthy replicas on other Ready nodes.
2. Stop k3s/container processes so the database cannot rewrite the selected files.
3. Delete all contents of those store volumes, preserving their empty root
   directories and ownership. Save file/byte counts before and zero entries after.
4. Cut VM power through libvirt and observe Kubernetes NotReady.

Only the selected worker's AAT `role=store` database volumes are erased. The OS,
control-plane storage, and other services' volumes are retained. This reproduces
loss of that worker's HazardEvent storage, rather than formatting its entire OS
image. It remains a local development scenario, separate from the official
Tester Agent exam.

At rejoin, Enter starts the VM with empty database volumes. Patroni must rebuild
those stores from surviving replicas; the runner verifies rebuilt PostgreSQL
files, data placement on the rejoined worker, and redundancy before losing the
second worker. Read/write and metrics probes run throughout shutdown, rebuilding,
and recovery verification. Every acknowledged continuity write is checked again
after monitoring ends. Actual node states are observed, never patched.

Only `aat-worker-1` and `aat-worker-2` in the known local Vagrant/libvirt lab are
allowed. The control plane and unexpected volume layouts are refused. Per-action
logs, storage plans, erasure proofs, rebuild proofs, and observed node states are
saved in the evidence folder. VM power-off uses `virsh destroy` (power cut without
undefining the VM), avoiding the provider's graceful-shutdown timeout.
For the official external procedure, set `CONFIG.automatic_worker_actions = False`;
external erasure is not automatically certified. k3d does not support this
VM storage-erasure procedure.

On failure or interruption, the runner attempts to restart the selected worker
and its k3s agent while preserving the failed result. If that recovery fails, restore
power with `bash scripts/vm/main.sh start-worker aat-worker-1` from infrastructure
(or worker 2), then, if needed, run `vagrant ssh aat-worker-1 -c
'sudo systemctl start k3s-agent'` from infrastructure/local. Restore redundancy
before restarting the full test.

These smaller defaults speed up development runs while preserving pagination
beyond 50 events and the two-loss sequence. Regenerating the small fixture does not
delete older records from the running database; full-store queries still scan
those records using larger pages. Changes to `CONFIG` and regenerated files
should be used in a new run: stop an old run with Ctrl+C, restore any disrupted
workers, then restart `task m2:p1`. Worker store erasure occurs only at the clearly labeled loss prompts.

It uses infrastructure `.env.runtime` and `.state/kubeconfig` by default.
`AGGREGATOR_TOKEN` and the separate `AUDIT_TOKEN` must be configured. Environment
variables override credentials in the env file; keep secrets out of the Python
configuration. Requires an interactive terminal, Python 3.10+, kubectl,
metrics-server, and at least three Ready nodes for this Citus deployment. The
fresh catalog must contain at least 51 unique events to exercise pagination.
Run one P1 test at a time on an isolated demo stack.

With `CONFIG.url = None`, the script opens and cleans up a localhost port-forward
to a Ready Aggregator on the control-plane node. This survives the selected
worker losses. To use an existing stable connection, set `url` to an Aggregator
origin such as `"http://127.0.0.1:8083"`. Load-balancer behavior belongs to M2 P2.

## Full guided demo

The bundled files are ready for a development run. For official proof, adapt the
assistant's actual catalog to the existing [catalog import contract](../../../README.md#import-katalog-gempa)
(JSON array or NDJSON) after confirming its format. The runner's external
reference representation is a JSON object containing the supplied count/checksum:

```json
{
  "source": "BMKG",
  "count": 12345,
  "checksum": "<64 lowercase hex characters from the assistant>"
}
```

An external reference without a `scope` field covers the **entire BMKG source**.
The count above is illustrative. Only the explicitly marked bundled development
reference uses catalog scope and a self-generated checksum. A fresh isolated
dataset is required to prove new catalog writes; reruns may replay existing
records to continue debugging and verify preservation/placement, with reuse
explicitly recorded.
Feeds may continue running during checks. Exact whole-source checks require
unchanged checksum bookends; otherwise source growth is explicitly recorded.
The chosen historical chart range must settle for its exact comparison. Do not
disable the Tester Agent. The suite does not modify application settings.

During the test, Enter starts the next step. Worker-loss/rejoin steps automatically
erase the selected worker's AAT store data, cut power, then start and rebuild it while checking the real node state. Use manual
worker actions only when following the official external disruption/disk procedure.

The script performs:

1. Import new catalog records while sampling read availability and per-node/pod
   CPU and memory. Verify all catalog references exist and their logical copies
   and primary writes are spread across nodes.
2. Reconcile a baseline across all sources: validate every audit/recent page,
   compare unchanged records across the live exports, check latest-50 ordering
   and omission of higher-ranked stable records, verify the exact catalog
   reference, and reconcile every chart dimension/bucket for the selected
   historical range. Independently verify source checksums whose bookends stayed
   unchanged. Inspect live replica locations for every catalog event and this
   run's surge/continuity writes.
3. Load a synthetic earthquake surge into one geographical region (Jawa), then
   repeat the checks and inspect placement for the surge alone. Surge IDs belong
   to this run; only these IDs are excluded when matching the assistant reference.
4. Keep independent read/write probes and CPU/memory sampling running while
   the first worker's store data is erased and it is powered off. Wait for
   NotReady, verify failover, and match the catalog checksum.
5. Reconcile original identities, controlled catalog contents, and acknowledged
   writes. Enter starts the first worker again. The suite waits for Ready and
   rebuilt PostgreSQL files and complete replica locations before allowing
   the next loss.
6. Lose the **other** worker and repeat preservation checks, then start it again
   and verify redundancy.

The suite records empty store-volume and rebuild evidence for this local scenario.
Official practice/exam logs and assistant dataset provenance are still required;
`full_demo_certified` stays false. See the
[Citus recovery procedure](../../../README.md#milestone-2-citus-storage).

## Coverage and limits

| PDF criterion | Automated checks | Evidence still requiring the operator |
| --- | --- | --- |
| P1.1 distribution | Exhaustive catalog/run-write locations, primary counts per node, no node containing the whole controlled dataset | Dataset provenance, actual disk utilization and physical topology |
| P1.2 uneven load | Catalog area histogram, per-node/pod CPU/memory samples during import and one-region surge; surge placement | Grafana disk/write IO, compare and explain imbalance |
| P1.3 global reads | Every page's order/tie-breaking, repeated-version handling, identity duplicates, cursor loops; stable-cohort completeness/latest-50 checks; exact full comparison if unchanged | Client API/demo API views |
| P1.4 node and disk loss | Bounded reads, explicit partial metadata, checksum count/format checks during disruption and exact catalog checksum after recovery, all-event preservation after recovery | Official node/disk disruption and assistant reference provenance |
| P1.5 empty-node rejoin | Concurrent reads/writes, target Ready state, restored replica locations | Video of node rejoining; replay load if demonstrating new capacity |
| P1.6 restored resilience | Require full replica locations before a second different worker loss; verify all old events and acknowledged writes survive | Official second-loss scenario logs |
| P1.7 dashboard | Exactly compare total, five type counts, severity, area, and UTC buckets against unchanged Audit data in the printed historical window | Browser charts, date controls, actual Client API view, visible incomplete/stale UI during failure |

Locations measure logical event placement, not stored bytes or disk IO. Primary
event counts describe placement of newly imported writes, not write throughput.
Metrics-server measures CPU/memory and may cache samples; the suite requires at
least two distinct node metric timestamps for unaffected nodes. Add Grafana's
Node utilization and storage/write IO evidence for the report. A passing API
reconciliation does not certify the browser UI or automatically cover all five
hazard categories with nonzero data; supply representative data and show the UI.

During a disruption, a read must finish within `CONFIG.max_read_seconds` (default 7s,
a runner bound, not a P1 threshold). HTTP failures/timeouts, missing availability
metadata, false complete results, and invalid partial checksum/analytics are
failures. HTTP calls have an 8s socket timeout by default. Writes rejected or
with unknown outcome during loss are counted separately; every acknowledged write
must survive. During rejoin, any failed write is a failure. A transiently rejected
write is never counted as acknowledged or claimed durable.

Catalog and regional loads last at least `CONFIG.load_seconds` (default 20s) to span
metrics collection windows. Increase `CONFIG.surge_events` to increase the regional
load; `CONFIG.batch_size` defaults to 100 (max 200). Monitors remain open for at least
20s, and Kubernetes metrics are sampled every 5s. If metrics-server has not yet
provided two distinct timestamps, sampling continues until it refreshes. Keep action prompts open
longer if metrics have not refreshed or the node has not changed state. Recovery
checkpoints and partial replica-location lookups keep retrying until the checks
pass, without a recovery/catch-up deadline. They still require complete replica
responses and copies on distinct nodes. Press **Ctrl+C** to stop; an interrupted
run is recorded as interrupted, never passed.

Progress and results use a title followed by two-space-indented details. Messages
from monitor threads are printed together so their lines stay grouped.
Each passed stage prints numerical `[RESULT]` summaries: reference count/checksum,
per-node placement, global-order checks, chart counts, erased/rebuilt volumes,
and availability/read-latency/write counters. The run ends with all stage statuses.
The terminal also reports catalog batches submitted/changed, exported page/event
counts, replica placement counts and percentages, elapsed time, and the current
event/live units while waiting. During a load or recovery action, the monitor
prints read/write/metrics counters every ten seconds. Recovery retries show their
attempt number and elapsed time. Per-request network timeouts and read-latency
checks remain, so a hung request does not stall progress reporting indefinitely.
Reads are sampled; this does not prove every request any client could send is
available.

## Evidence and exit status

Each run creates `docs/evidence/m2-p1-<UTC>-<random>/`; `CONFIG.output_dir` must select
a new folder. Existing evidence is never overwritten. Artifacts include:

- `result.json`: selected automatic check results, failures, pending manual evidence.
- `http.jsonl`: endpoint, method, query, UTC time, duration, JSON response or error
  class. Request credentials/headers and login bodies are not recorded.
- `<checkpoint>.json`: full canonical export, checksum, analytics reconciliation,
  all-source canonical exports, live-comparison scope/results, exhaustive catalog/run-write
  placement where redundancy is expected, and sanitized topology.
- `<window>-metrics.jsonl` and `<window>-monitor.json`: CPU/memory samples, target
  state observations, successful/partial reads, accepted/rejected writes, failures.
- Catalog manifest, catalog placement, copied reference and temporary port-forward log.
- `dashboard-analytics.json`: final Group 3 comparison, actual date range, source
  growth/verification statuses, and pending browser evidence.

A stable-cohort check is not proof that separate live requests shared a database
snapshot; `global_reads.exact_whole_dataset_comparison` records whether the full
exports stayed identical. Development fixtures establish three hazard categories with nonzero historical
counts; wildfire and coastal examples require their real sources. Retain separate browser
and official dataset evidence; `full_demo_certified` remains false.

Exit 0 means the **selected automatic checks** passed. `full_demo_certified` stays
false and manual evidence stays listed after the guided run. Exit 1 means a failed or
interrupted run. Supplying CLI arguments is rejected with exit 2. Retain failed artifacts; after recovery
restore the workers before restarting. Existing catalog records can be replayed;
use a fresh isolated dataset when collecting proof of new writes. Do not overwrite evidence or claim an interrupted run completed. Synthetic events remain in the isolated demo dataset; the loss steps erase worker-local store copies, which must rebuild from surviving replicas.
