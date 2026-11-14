# Local development: Go demo and Signal Deck

ClickHouse and the OpenTelemetry Collector run in Docker Compose. Go demo, the
telemetry API, and Vite run natively. No Kubernetes or GitLab is required.

## Start

Prerequisites: Colima (or another Docker runtime), Docker Compose, Go with
automatic toolchain downloads enabled, Python 3, Task, and pnpm. The frontend
pins pnpm 12.6.0 and downloads its Node 24 runtime; the backend's `go.mod` pins
its Go toolchain. Initial setup needs network access.

```sh
# macOS: a practical starting allocation, not the stack's minimum requirement.
colima start --cpu 4 --memory 6
cd code/apps
task dev:setup
task dev
```

If Colima is already running, its allocation is unchanged by `start`. Stop it
before resizing, after stopping workloads you want to preserve.

Open [Go demo](http://127.0.0.1:8081/demo) and click Success, Slow, or Error.
Each response includes a trace ID and a link to its trace in
[Signal Deck](http://127.0.0.1:5173/traces). Open the trace to inspect the request,
child span, and correlated logs. Typical visibility in local testing was 2–3
seconds; the intentional error action returns HTTP 500.

The launcher checks ports and readiness, builds both Go apps, and starts Vite.
Before starting Docker, it checks pnpm launchers from PATH, PNPM_HOME, and the
standard macOS/Linux standalone locations against the project's pinned version.
It prints and reuses the first working absolute path for setup/startup, skipping
broken Corepack shims. If no launcher works, it reports the launcher errors.
Native processes that exit during startup fail immediately with their exit code
and log path instead of waiting for the readiness timeout.
Go file/module edits trigger rebuilds and graceful restarts. Failed builds keep
the last working process and write diagnostics to `dev/.runtime/*.log`. Vite
provides frontend hot reload. No application images are built.

## Commands

Run from `code/apps`:

| Command | Purpose |
| --- | --- |
| `task dev:setup` | Pull pinned images, validate Collector config, install locked native dependencies |
| `task dev` | Start dependencies and native apps; watch Go source changes |
| `task dev:status` | Show native listener PIDs, URLs, health, source/log paths, and Docker containers |
| `task dev:bot` | Continuous randomized demo requests; Ctrl-C stops only the bot |
| `task dev:smoke` | Send three actions and verify trace/log correlation through the API |
| `task dev:test` | Go tests/vet and frontend production build/typecheck |
| `task dev:integration` | Live database query, tenant-isolation, cancellation, and write-denial tests |
| `task dev:stop` | Stop the supervisor and dependency services it started |
| `task dev:reset` | Stop apps and explicitly delete this Compose project's telemetry volume |

Ctrl-C performs the same cleanup as `dev:stop`: app exporters flush before
owned dependencies stop. Dependencies already running when the supervisor
starts are preserved. To stop those too, run `docker compose down`.
`docker compose down` retains history; `docker compose down --volumes` deletes it.
Neither command stops Colima itself. Native logs are appended under the ignored
`dev/.runtime` directory; delete old log files when the apps are stopped if needed.

## Configuration

| Component | Address / settings |
| --- | --- |
| Go demo | `127.0.0.1:8081` |
| Telemetry API | `127.0.0.1:8080` |
| Vite | `127.0.0.1:5173`, proxies `/api` to Go |
| OTLP HTTP | `http://127.0.0.1:4318` |
| Collector health | `http://127.0.0.1:13133/` |
| ClickHouse native | `127.0.0.1:9000` |
| ClickHouse HTTP | `http://127.0.0.1:8123` |
| Database | `otel` |
| API user/password | `app` / `local-clickhouse-app` |
| Ingest user/password | `otelcollector` / `local-clickhouse-otel` |

All published host ports bind to loopback. The Collector connects to
`clickhouse:9000` inside Compose. No gRPC port is exposed. Credentials are
explicitly disposable local defaults. The API user has SELECT-only grants;
its readonly profile allows timeout settings required by the Go driver.

The launcher configures the demo with:

```sh
PORT=8081
LISTEN_HOST=127.0.0.1
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_SERVICE_NAME=go-demo
OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local,tenant.id=local
```

Go demo works standalone without an OTLP endpoint and retains its JSON greeting
at `/`. With export enabled it sends both traces and contextual slog records.
The Collector stamps both signals with `tenant.id=local` and the local environment.
It does not tail stdout, avoiding duplicate records. Health checks are excluded
from demo telemetry. API self-export is off in the launcher to keep UI polling
out of the demo dataset; manually started API processes retain optional trace export.

## Resources and retention

ClickHouse is limited to two CPUs / 2 GiB; Collector to one CPU / 256 MiB, with
bounded queues and a 128 MiB memory limiter. Tables have 24-hour retention,
cleaned asynchronously. These limits are not the total workstation requirement.
Colima's VM, native Go/Node processes, and compilation consume additional memory.
A light-load sample measured approximately 624 MiB for ClickHouse and 216 MiB
for the Collector; usage varies with activity. Container logs rotate at 10 MiB.
Queued telemetry can be lost on a crash or a prolonged database outage.

## Diagnose

```sh
docker compose logs --tail=100 otel-collector clickhouse
curl --fail http://127.0.0.1:13133/
curl --fail http://127.0.0.1:8080/readyz
docker compose exec clickhouse clickhouse-client --user app --password local-clickhouse-app --query 'SHOW TABLES FROM otel'
```

Collector health alone does not prove ingestion; run `task dev:smoke`.
If a port is occupied the launcher fails without killing that process.
If the database is unavailable, the API returns 503 and the UI displays the
failure instead of substituting empty results. Polling recovers after restart.

On macOS, if pnpm 11 fails to switch to the project's pinned pnpm with ENOEXEC,
complete the pinned package's `install.js` using Node (the path is shown in
pnpm's error), or install pnpm 12.6.0 with its install scripts enabled. Do not
change the project pin to bypass the error.

Images match the platform versions. Schema changes on upgrades may require a
migration or an explicit disposable-volume reset. Collector queue batching
follows the [pinned exporter documentation](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.160.0/exporter/clickhouseexporter).
See the [implementation checklist](../telemetry-ui/LOCAL_DEVELOPMENT_PLAN.md)
and [API contract](../telemetry-ui/API.md).

## Automatic demo traffic

Start the stack with `task dev`, then in another terminal:

```sh
cd code/apps
task dev:bot
# Finite, reproducible run:
task dev:bot -- --count 100 --interval 0.5 --seed 42
```

The bot is a native Python process with no packages to install. Default traffic is
70% success, 20% slow, and 10% intentional HTTP 500 responses. It sends one request
at a time, then waits 0.5–1.5 seconds by default; `--interval` changes the average
pause, not a guaranteed requests-per-second rate. Slow calls vary from 250–1500 ms.
Ctrl-C stops the bot after any in-flight request finishes (5-second timeout), and
prints a summary. Expected HTTP 500s count separately from unexpected failures.
A finite run exits nonzero if any unexpected transport/status/response errors occur.
Use `--url http://127.0.0.1:8081` to override the target. No redirect following or
external proxy is used. The bot does not start Docker or the applications itself.

Each request varies synthetic customer, region, product, quantity, and delay.
Find these on server and work spans and action-log attributes: `demo.source=bot`,
`demo.customer_id`, `demo.region`, `demo.product`, `demo.quantity`, `demo.delay_ms`.
`demo.payload` contains JSON with `customer.id`, `customer.region`, `cart.product`,
and `cart.quantity` for JSON-path filters. Existing manual buttons still work.
Parameters are validated: delay 0–2000 ms, quantity 1–10, and bounded categorical
values. Invalid inputs return 400 without simulated work.

In Signal Deck select a recent time range and **Resume live** to see charts change.
Applying search pauses refresh as before; resume it when you want ongoing updates.
