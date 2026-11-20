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
| `url` | `None`: auto-select VM control-node InternalIP on port 30088, or `http://127.0.0.1:30088` on k3d. Set an explicit Traefik origin to override. |
| `source` | `BMKG`, whose owner is killed/frozen |
| `worker` | `None`: automatically choose a Ready lab worker without mocks/coordinator, for VM clock/freeze tests |
| `observation_seconds` | 30 seconds of polling observations; at least three mock event intervals |
| `takeover_seconds` | 60-second maximum from fault request to observed successful successor poll |
| `freeze_seconds` | 30 seconds; must exceed the 20-second DB session idle timeout |
| `event_intervals` | BMKG/PVMBG 10 seconds; must match running mock configuration |
| `load_vus`, `load_seconds` | Full `TestConfig()` defaults: **10,000 VUs, 120 seconds per measured run** |
| `scaling_seconds` | `None` uses `load_seconds`; set a longer scaling window while keeping throughput samples short |
| `warmup_seconds` | Warmup before each run; full default 10 seconds |
| `rolling_image` | `None`: select a different published GitOps pin from local Git history; or specify an immutable `repository@sha256:...` |
| `rolling_seconds`, `rolling_rps` | 360 seconds at 30 aggregate offered requests/second, with the same 10,000 VUs |

The checked-in `CONFIG = TestConfig()` runs **all three groups and all eight
criteria**. Scaling uses **10,000 VUs for 120 measured seconds**, followed by two
120-second throughput samples at one and three replicas. The mandatory version
rollout uses the same 10,000 VUs for 360 measured seconds, paced at 30 aggregate
RPS so the zero-failed-request criterion can be tested below the unchanged
admission limit. Every VU must send a measured request; sleeping unused VUs do not
qualify. Each run has a 10-second warmup. Generator traffic takes about **12 minutes
40 seconds**, plus readiness, image downloads, Audit exports, polling observations,
and the guided clock/fault actions.

Scale transitions must finish during measured traffic, with at least five seconds
after each transition. Increase `scaling_seconds` if pods start too slowly; waits
are bounded by the remaining measurement window. `rolling_seconds` is separate
and must allow every VU a paced request plus ten seconds of margin. A different
actual image is mandatory: missing candidates, unavailable images, failed or
rejected requests, and unexercised VUs fail the rollout check instead of skipping it.

Small/subgroup runs explicitly remain development evidence. `result.json` records
all eight check statuses, the sustained-load minimum, and whether all automated
criteria completed. The error-rate and **2.0x throughput** requirements remain the
same. 10,000 VUs can consume substantial CPU/memory on the generator; active-VU
checks do not prove it is free from CPU/network limits. Run k6 outside application
pods and retain host resource evidence.

The default full suite requires the VM lab for separate-kernel clock/freeze
checks. On `task up -- --novm` (k3d), explicitly select groups without Group 2 for
development only; a full configuration fails preflight rather than silently
skipping those criteria. The script never adjusts the shared Docker clock.

The VM clock test is guided: the operator disables synchronization, applies +45 s,
then -30 s relative to the original clock, and restores synchronization in the
final prompt. The runner measures the node offset against its own midpoint clock,
with a bounded measurement RTT. Keep source mocks and coordinator off that worker.
Save the actual clock/Tester Agent commands separately. This is a worker-clock
scenario, not a test of skewing the database authority itself.

Before each clock/freeze check, the runner prepares the actual source owner on the safe worker
by restarting owner pods until an observed successor there has a newer epoch.
Preparation is bounded and recorded separately from the measured freeze. Enter
suspends the known local Vagrant/libvirt worker and resumes it in `finally`.
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
The Aggregator Deployment configures a shared hazards/analytics token bucket of
20 reads/second with a burst of 5 **per replica**, before the cache. The same
settings apply at 1, 3 and 5 replicas. Throughput evidence reads these two public
environment values from every Ready pod before/after each sample and rejects
configuration differences. A capped ratio measures configured admission capacity,
not maximum uncapped throughput; retain this distinction in the report. Controlled
rejections are allowed by P2.6, but this does not guarantee that a cap-derived
ratio will be accepted as P2.7 capacity evidence. P2.8 still requires zero 429s;
the separate paced workload stays below the aggregate admission capacity without
changing the cap. Its measured offered RPS and number of exercised VUs are saved.
Both comparison runs advertise `Accept-Encoding: gzip` and validate the decoded
JSON; response bodies are not discarded. Keep the same compression negotiation
when comparing results so network bandwidth does not change between samples.

A temporary authenticated HTTPRoute forwards a run-specific hostname to the
Aggregator Service through Traefik. Requests use the Aggregator token and the
same URL, query, VUs, and resource limits for the 1/3-replica comparison. They do
not traverse Client/Auth, so a singleton Client/Auth does not conceal Aggregator
scaling. This is explicitly an Aggregator benchmark; add end-to-end Client load
evidence separately. A port-forward to one pod/service is not a valid substitute
for this routing test. The selected mode and gateway URL appear before the first
prompt and are saved in `result.json`. For a custom gateway/network, set `url` to
its reachable Traefik origin; the existing `http://client.localhost` listener is a
different hostname. Route readiness has a maximum 30-second budget, accepts a
complete empty dataset, and reports the last connection/HTTP error on failure.

The runner first disables automated sync on the root, dev parent, and Aggregator
Application, drains active/queued sync operations, then sets `skip-reconcile`.
It restores the original image/replica count before restoring each original
auto-sync policy and annotation, then removes its route. The throughput comparison
records actual running image IDs and a pod-template hash before/after each sample;
an image/configuration change invalidates the comparison instead of reporting a ratio.
Other cluster changes should not be made concurrently. HPA must be disabled for
these manual-replica tests. Preflight selects a different published digest and
preloads it onto **all Ready nodes**, including when the Deployment uses pull
policy `Never`. Temporary preload pods run only `sleep`, so they cannot become
extra pollers. They are removed after preloading or failure. Missing image history
or unavailable images fail preflight; set `rolling_image` explicitly if needed.
A same-image restart does not satisfy P2.8. The original Deployment template and
replica count are restored after the run. Mock polling stays live throughout P2;
do not run the P1 polling-freeze task at the same time.

Evidence is written to `tests/m2/p2/evidence/m2-p2-<timestamp>-<id>/` (ignored by Git):

- `result.json`: individual pass/fail/skip, measurements, cleanup errors, limits.
- `deployment-baseline.json`: original image, resources, strategy and node kernels.
- `throughput-*-deployment.json`: image IDs and pod-template fingerprints around each sample.
- `polling.jsonl`: filtered source/upstream logs with pod UID and runner observation time.
- `*-audit.json`: logical identity preservation and deterministic mock-gap checks.
- `*-k6.json`, `*-k6.log`: per-window load results and generator output, without tokens.
  HTTP status counters distinguish transport errors (status 0) from HTTP errors.
- `scale-transitions.json`: requested and Ready times for each replica transition, including failed runs.
- `kubectl-errors.jsonl`: bounded/redacted stderr when a Kubernetes command fails.
- `progress.jsonl`: timestamped phases, wait durations, budgets and last observed state.

Each k6 run prints an indented `[RESULT]` block with measured successful/total
RPS, request counts, controlled rejections, unexpected error rate and exercised
VUs. These exclude warmup and also print before validation when a k6 summary is
available for a failed run. The throughput comparison prints both successful RPS
values, the ratio and the >=2.0x verdict; rollout prints its zero-failure result.

While an automated stage or cleanup is waiting, `[WAIT]` appears every five
seconds. It names the current operation: owner takeover, Ready replicas, Audit
pages, VM actions or the active k6 run. Ready counts and retry/reconciliation
details show the last observation. Messages pause at Enter prompts. A heartbeat
shows that the runner is waiting; it does not claim the underlying condition has
advanced or passed. Existing timeouts and pass/fail thresholds are unchanged.

Each generator log is capped at 2 MiB; repeated connection errors are counted in
the k6 summary instead of filling the disk with warning lines. If the local stack
cannot stay reachable at 10,000 VUs, first debug Group 3 with `groups=(3,)`,
`load_vus=100`, `load_seconds=20`, and `scaling_seconds=60`. That is development troubleshooting only;
it does not satisfy the PDF load minimum or replace a full-capacity run.

The deployment's coordinator connection budget matters during scaling. The
Kubernetes default is `DB_MAX_CONNS=7` per replica: 5 source/background sessions,
1 shared read slot and 1 read slot reserved for priority reads (`READ_PRIORITY_SLOTS=1`,
Field Team/BNPB Internal via the Client API). Five replicas use at most 35 of the
50 PostgreSQL coordinator slots, before Patroni/internal connections. The k6 load
calls the Aggregator directly without `X-Read-Class`, so it only ever uses the shared
slot (the same per-pod capacity as before the priority slot was added) and the page
cache in front of it; the reserved slot neither adds to nor takes from the measured
throughput. This leaves headroom; it does not by itself prove the error-rate or
throughput criteria. Retain database error evidence when
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
always remains false: official fault/storage proof, generator capacity evidence,
and the report still require review. `automated_criteria_complete` becomes true
only when all eight checks pass at the required load size on VM nodes.

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
