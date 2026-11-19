# Milestone 2 — Problem 2

Like P1, `test-problem-2.py` has one `CONFIG`, three ordered groups, Enter prompts,
and one evidence directory per run. Run one suite at a time on an isolated lab:

```sh
cd code/infrastructure
task m2:p2
```

Requires Python 3.9+, kubectl, a Ready three-node AAT cluster, Traefik's Gateway,
Argo CD Applications `platform`, `dev-apps`, `aat-aggregator-dev`, and k6 for
Group 3. Tokens come from `.env.runtime` or environment overrides. The separate
`AGGREGATOR_TOKEN` and `AUDIT_TOKEN` are never placed in subprocess arguments or
evidence. No new fixture dataset is needed: this suite reconciles live mock IDs.

## Configure first

Edit `CONFIG` at the top of `test-problem-2.py`:

| Setting | Default / purpose |
| --- | --- |
| `groups` | `(1, 2, 3)`; choose `(1,)` for coordination/failover first |
| `url` | `http://127.0.0.1:30088`, the published Traefik NodePort |
| `source` | `BMKG`, whose owner is killed/frozen |
| `worker` | `aat-worker-1`, only for VM clock/freeze tests |
| `observation_seconds` | 30 seconds of polling observations; at least three mock event intervals |
| `takeover_seconds` | 60-second maximum from fault request to observed successful successor poll |
| `freeze_seconds` | 30 seconds; must exceed the 20-second DB session idle timeout |
| `event_intervals` | BMKG/PVMBG 10 seconds; must match running mock configuration |
| `load_vus`, `load_seconds` | **10,000 VUs for 120 seconds per measured run**, plus 10-second warmup |
| `rolling_image` | Set a different immutable `repository@sha256:...` image, available on all target nodes |

Group 3 runs scaling, a one-replica baseline, a three-replica comparison, and an
optional rolling-update run. Expect at least 6.5 minutes of measured/warmup traffic
before the optional fourth run, plus setup/recovery. Smaller development settings
are allowed by reducing VUs, but evidence then explicitly says
`pdf_load_minimum_met: false`. Group 3 keeps at least 120 seconds even for local
debugging so pod readiness and both scale transitions fit inside the load window.
10,000 VUs can consume substantial CPU/memory on the generator; active-VU/window
checks do not by themselves prove the generator is free from CPU/network limits.
Run the generator outside application pods and retain host resource evidence.

On Mac/k3d, begin with `groups=(1,)`. Both VM clock/freeze checks are **SKIP** on
container nodes. All k3d results are development observations, not qualifying
separate-kernel demo evidence. The script never adjusts the shared Docker clock.

The VM clock test is guided: the operator disables synchronization, applies +45 s,
then -30 s relative to the original clock, and restores synchronization in the
final prompt. The runner measures the node offset against its own midpoint clock,
with a bounded measurement RTT. Keep source mocks and coordinator off that worker.
Save the actual clock/Tester Agent commands separately. This is a worker-clock
scenario, not a test of skewing the database authority itself.

The freeze test requires the selected source's current owner to be on the selected
non-control-plane worker. Choose `source`/`worker` accordingly before running.
Enter suspends the known local Vagrant/libvirt worker and resumes it in `finally`.
There is no disk erasure. Non-Vagrant/native environments need the external
official procedure rather than pretending Docker pause is an equivalent demo.

## Groups and evidence

| Group | Criteria | Checks |
| --- | --- | --- |
| 1: coordination | P2.1–3 | Three Ready replicas on distinct nodes, ready EndpointSlices, actual routed responses from three pod UIDs, upstream/caller correlation joins, single-owner epochs, overlapping upstream requests, polling request-rate bound, logical event uniqueness, instance deletion and takeover within 60 seconds |
| 2: clock/freeze | P2.4–5 | Measured +45/-30 offsets, continued polling/audit reconciliation, worker freeze longer than owner timeout, successor epoch, no old-epoch successful poll observed after resume |
| 3: load | P2.6–8 | k6 5-second traffic/VU windows, 3→5→2 replicas observed Ready during load, <1% unexpected errors excluding validated 429 responses, successful throughput ratio ≥2.0, different actual image IDs after rolling update with zero failed/rejected requests |

The workload retries controlled 429 responses after `Retry-After` (capped at five
seconds per iteration). Rejections remain recorded separately; successful RPS
counts only complete 200 responses. VUs stay allocated during the retry delay.

A temporary authenticated HTTPRoute forwards a run-specific hostname to the
Aggregator Service through Traefik. Requests use the Aggregator token and the
same URL, query, VUs, and resource limits for the 1/3-replica comparison. They do
not traverse Client/Auth, so a singleton Client/Auth does not conceal Aggregator
scaling. This is explicitly an Aggregator benchmark; add end-to-end Client load
evidence separately. A port-forward to one pod/service is not a valid substitute
for this routing test. VM deployments may set `url` to their reachable Traefik
origin; the existing `http://client.localhost` listener is a different hostname.

The runner temporarily sets Argo's `skip-reconcile` annotation on the root,
dev parent, and Aggregator Application, waiting for active syncs to finish. This
prevents self-heal from undoing replica/image mutations. It restores the original
image/replica count before restoring those annotations, then removes its route.
Other cluster changes should not be made concurrently. HPA must be disabled for
these manual-replica tests. Local image pull policy `Never` requires pre-importing
the candidate rolling image into **all nodes**; a missing/unavailable image must
fail rather than being counted as a successful rollout. A same-image restart
does not satisfy P2.8; without a candidate that criterion is SKIP.

Evidence is written to `docs/evidence/m2-p2-<timestamp>-<id>/`:

- `result.json`: individual pass/fail/skip, measurements, cleanup errors, limits.
- `deployment-baseline.json`: original image, resources, strategy and node kernels.
- `polling.jsonl`: filtered source/upstream logs with pod UID and runner observation time.
- `*-audit.json`: logical identity preservation and deterministic mock-gap checks.
- `*-k6.json`, `*-k6.log`: per-window load results and generator output, without tokens.
  HTTP status counters distinguish transport errors (status 0) from HTTP errors.
- `scale-transitions.json`: requested and Ready times for each replica transition, including failed runs.
- `kubectl-errors.jsonl`: bounded/redacted stderr when a Kubernetes command fails.

Each generator log is capped at 2 MiB; repeated connection errors are counted in
the k6 summary instead of filling the disk with warning lines. If the local stack
cannot stay reachable at 10,000 VUs, first debug Group 3 with `groups=(3,)`,
`load_vus=100`, and `load_seconds=120`. That is development troubleshooting only;
it does not satisfy the PDF load minimum or replace a full-capacity run.

The deployment's coordinator connection budget matters during scaling. Five
Aggregators with `DB_MAX_CONNS=8` can consume all 40 PostgreSQL coordinator slots,
before Patroni/internal connections. The Kubernetes default is 6 per replica
(at most 30 for five replicas). This leaves headroom; it does not by itself
prove the error-rate or throughput criteria. Retain database error evidence when
debugging `too many clients` rather than weakening the load thresholds.

Audit checks protect identities from previous exports and reject repeated identical
event versions. Legitimate updates encountered twice across READ COMMITTED pages
may have a newer `ingested_at`, while keeping the same hazard identity. Gap checks
cover the interior of each measured wall-clock window using the current BMKG and
PVMBG timestamp-ID rules, with one event interval of margin on each edge and
bounded catch-up retries. They are a development-mock oracle, not an independent
checksum from an external source or a physical duplicate-row inspection.

Upstream overlap comparisons use timestamps from the **same mock pod**, not clocks
across Aggregators. Source mocks must remain stable during the observation.
Request counts include a small boundary/takeover allowance; they detect multiplied
steady pollers, not exactly-once delivery of an ambiguous remote HTTP request.
The runner joins existing upstream correlation IDs to the caller's owner/epoch;
it does not require an application logging change. Stream interruptions make load
evidence fail rather than silently certifying missing logs.

Clock/freeze observations cannot prove linearizability or force a pause at every
storage commit boundary. A delayed success log can also arrive after an earlier
commit; inspect such a failure against storage/Tester Agent traces. Retain the
fencing integration tests and official fault evidence. `full_demo_certified`
always remains false; skipped criteria and development-sized runs are explicit.

On Ctrl+C the runner terminates k6/log streams, resumes a worker it suspended,
restores deployment/GitOps settings, and removes its route. During a manual clock
step, restore the worker clock when prompted even if the check fails. SIGKILL,
power loss, or a second interruption can prevent cleanup: inspect `result.json`,
restore the original image/replicas, resume the selected worker, restore clock
sync, remove the recorded route, and restore the three original Argo annotations
before starting another run. Credentials and source/admin settings are not changed.

Offline checks (no live faults or load against the cluster):

```sh
python3 -m unittest discover -s ../apps/aat/tests -p test_m2_p2.py
k6 inspect -e P2_VUS=2 -e P2_SECONDS=15 -e P2_WARMUP=1 ../apps/aat/tests/m2/p2/load.js
```
