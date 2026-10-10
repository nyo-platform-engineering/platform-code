# Integration test scripts (Problem 1–5)

## Problem 1 — demo Python

Prerequisite: stack sudah berjalan lewat `docker compose up`.
Dari `code/apps/aat`:

```sh
python3 tests/test-problem-1.py
```

## Problem 2 dan 3

Keduanya membutuhkan Python 3 dan Docker Compose v2. Problem 2 juga memakai k6
lokal untuk load testing.

### Persiapan dan cara menjalankan

Buat `.env` terlebih dahulu:

```sh
python3 scripts/setup-env.py
```

Untuk Problem 2, atur kedua nilai ini di `.env`:

```dotenv
PVMBG_DELAY_MIN_MS=3000
PVMBG_DELAY_MAX_MS=3000
```

Pertahankan `START_TIME` dan kredensial yang sudah ada, lalu nyalakan stack:

```sh
docker compose -p aat-part1 --env-file .env -f compose.yaml -f compose.client.yaml up -d --build
python3 tests/test-problem-2.py
python3 tests/test-problem-3.py
```

Jika stack sudah berjalan saat delay diubah, buat ulang hanya PVMBG agar
konfigurasi baru diterapkan:

```sh
docker compose -p aat-part1 --env-file .env -f compose.yaml -f compose.client.yaml up -d --no-deps pvmbg
```

Alur pengujian ada di masing-masing file Python. `test_helpers.py` menyediakan
helper HTTP, pembacaan `.env`, pemeriksaan Docker, dan penyimpanan hasil.

### Problem 2: konkurensi dan ketersediaan

Test ini memeriksa:

- Delay PVMBG pada container yang berjalan tetap 3 detik, termasuk pengukuran
  langsung ke mock. Mengedit `.env` saja belum mengubah container yang aktif.
- Request BMKG-only dan PVMBG-only dikirim bersamaan, sebanyak 20 pasangan.
  BMKG p95 harus di bawah 300 ms. Client API membaca Canonical Store melalui
  Aggregator sementara polling kedua sumber tetap berjalan.
- Load 50 VU selama 65 detik, tanpa jeda antar-request dan memakai keep-alive.
  `scripts/load-test.py` menjalankan `scripts/load/test2.js` dan mencatat koneksi
  TCP paralel, throughput, p50/p95/p99, error rate, jumlah 429, serta kondisi
  container. Error di luar penolakan terkontrol harus di bawah 1%.
- Saat outage dinyalakan lewat `POST /admin/outage`, PVMBG mengembalikan 503.
  Polling BMKG tetap maju dan data seismik tetap fresh. Data vulkanik tersimpan
  masih dapat dibaca dengan `sources[].status=stale`, `stale=true`, dan
  `last_success_at`.
- Setelah outage dimatikan, PVMBG kembali fresh tanpa perubahan container ID,
  waktu mulai, atau restart count.

Outage dimatikan melalui `finally`, termasuk ketika pemeriksaan gagal. Jalankan
pada stack demo tanpa test lain yang ikut mengubah keadaan PVMBG.

### Problem 3: autentikasi dan hak akses

Test ini memeriksa:

- Kredensial BMKG dan PVMBG valid pada sumbernya sendiri, lalu ditolak dengan
  401/403 saat dipakai pada sumber lain.
- Media (`public`) hanya menerima tujuh field Ringkasan dari kedua sumber.
  Permintaan field Mentah, termasuk query campuran Ringkasan/Mentah, ditolak.
  Token Media juga ditolak oleh mock dan API internal Aggregator.
- Tim Lapangan (`responder`) login sekali dan membaca data setiap lima detik
  sampai access token kedaluwarsa alami. TTL default adalah 60 detik. Refresh
  menghasilkan token baru dengan hak akses yang sama, sementara token lama
  tetap ditolak. Penggunaan ulang refresh token lama mencabut sesi tersebut.
- Kredensial berbeda antar-domain, `.env.example` ter-track, dan `.env` diabaikan
  Git. Nilai secret saat ini diperiksa pada file AAT dan riwayat Git AAT yang
  masih dapat diakses. Kode Go produksi juga diperiksa untuk literal kredensial.

Audit kredensial ini bukan pemindai secret lengkap. Kredensial lama yang berbeda
dari nilai `.env` saat ini masih perlu diperiksa terpisah. Kredensial khusus unit
test dan string protokol `Bearer` diperbolehkan.

### Hasil dan konfigurasi

Setiap run membuat folder baru `docs/evidence/problem-2-<id>/` atau
`problem-3-<id>/`. Hasil utama ada di `result.json`; hasil k6 ada di subfolder
`load/`. Token dan secret tidak disimpan dalam hasil.

Request penting ditampilkan sebagai `[HTTP]` dengan endpoint, status aktual, dan
latensi. Bagian `http_results` di `result.json` merekam hasil yang sama beserta
waktu request, correlation ID request/respons, dan ringkasan respons. Problem 2
juga menampilkan delay terukur, p95 BMKG, status sumber selama outage, dan kondisi
container sebelum/sesudah. Problem 3 menampilkan contoh record Media, field yang
diterima, waktu expiry, serta status refresh dan token lama. Body login/refresh
dan nilai token tidak dicetak. Hasil lama tetap tersedia; jalankan ulang test
untuk menghasilkan bukti dengan detail tambahan ini.

```sh
python3 tests/test-problem-2.py --vus 50 --seconds 65
python3 tests/test-problem-3.py --timeout 120 --output-dir docs/evidence/problem-3-demo
python3 tests/test-problem-2.py --help
```

`--output-dir` harus menunjuk folder baru agar hasil sebelumnya tetap tersimpan.
Argumen CLI mengalahkan nilai environment.

| Environment | Default | Keterangan |
| --- | --- | --- |
| `PROJECT` / `COMPOSE_PROJECT_NAME` | `aat-part1` | Nama project stack; `COMPOSE_PROJECT_NAME` diprioritaskan |
| `ENV_FILE` | `.env` | File konfigurasi |
| `TIMEOUT_SECS` | `90` | Batas waktu menunggu kondisi polling |
| `CURL_TIMEOUT_SECS` | `10` | Timeout request; harus lebih dari 3 detik |
| `LATENCY_SAMPLES` (P2) | `20` | Minimal 20 pasangan request |
| `LOAD_VUS` (P2) | `50` | Minimal 50 VU |
| `LOAD_SECONDS` (P2) | `65` | Minimal 60 detik |

Untuk laporan, jelaskan goroutine polling yang terpisah, batas konkurensi
Client/Auth, pool DB, timeout, dan freshness Canonical Store pada Problem 2.
Pada Problem 3, jelaskan allowlist field di server, pemisahan kredensial, token
acak yang disimpan sebagai hash, TTL, rotasi, dan pencabutan sesi saat replay.
Detail tersedia di [kontrak client](../docs/client-contract.md) dan
[panduan load testing](../docs/load-testing.md).

### Jika test 2–3 gagal

- `START_TIME` belum ada: jalankan `python3 scripts/setup-env.py` sebelum
  menyalakan stack. Nilai yang sudah ada tetap dipertahankan.
- Docker tidak dapat diakses: buka Docker Desktop dan periksa `docker version`.
- Container tidak ditemukan: pastikan stack aktif dengan project, file Compose,
  dan `.env` yang sama dengan test.
- Delay PVMBG belum 3 detik: atur min/max di `.env`, lalu buat ulang PVMBG sebelum
  menjalankan Problem 2.

Pesan kegagalan menampilkan diagnostik perintah dengan nilai kredensial
disamarkan. Detail pemeriksaan ada di output terminal dan hasil JSON.

## Problem 4 & 5

Dua integration test Python yang runnable dan repeatable terhadap sistem Docker
yang sudah ada. Keduanya membuktikan requirement tugas tanpa me-rewrite aplikasi.

- `test-problem-4.py` — deployment independen, rebuild satu service,
  penyimpanan `HazardEvent` yang fleksibel, isolasi Canonical Store.
- `test-problem-5.py` — fan-out NATS JetStream ke dua consumer, isolasi
  failure + durable recovery, consumer ketiga via compose file kedua,
  delivery semantics / idempotency.

> Versi Python (`test-problem-4.py` / `test-problem-5.py`) adalah versi utama
> dan pengganti versi shell lama (`test-problem-4.sh` / `test-problem-5.sh`).
> File `.sh` tetap ada hanya sebagai referensi; tidak perlu dipakai lagi.

## Nama file aktual (hasil discovery, bukan karangan)

- Compose utama: `compose.yaml`
  services: `postgres`, `aggregator`, `bmkg`, `pvmbg`, `nats`,
  `dashboard-updater`, `field-notifier`.
  Networks: `storage` (internal, hanya postgres + aggregator),
  `services` (semuanya kecuali postgres).
- Compose kedua: `compose.consumer.yaml`
  service: hanya `test-consumer`. Ia join ke network `services` yang sudah ada
  dan dial ke broker yang sudah ada (`NATS_URL=nats://nats:4222` by default).
  Selalu pakai dengan chaining ke file utama di bawah nama project yang **sama**.

Overlay lain di repo (`compose.client.yaml`, `compose.scale.yaml`) tidak
dipakai oleh test ini.

## Prasyarat

- Docker Engine + Compose v2 (`docker compose version`) dan Python 3
  (stdlib saja: `subprocess`, `urllib`, `json` — tanpa `curl`/`jq` tambahan).
  Test Python berjalan di Linux maupun Windows.
- `.env` sudah ada (jalankan `python scripts/setup-env.py` sekali;
  pertahankan `START_TIME` agar stabil).
- Cukup disk untuk satu build image tambahan (`test-consumer`, `--no-deps` saja).

## Menyalakan stack utama

Dari `code/apps/aat`:

```sh
docker compose -p aat-part1 --env-file .env -f compose.yaml up -d --build
docker compose -p aat-part1 --env-file .env -f compose.yaml ps
curl -fsS http://127.0.0.1:8083/health        # aggregator (AGGREGATOR_PORT)
curl -fsS http://127.0.0.1:8222/healthz       # NATS monitoring
curl -fsS "http://127.0.0.1:8222/jsz?streams=true" | grep HAZARDS_STREAM
```

Host ports (default dari `.env`): BMKG `8081`, PVMBG `8082`, Aggregator
`8083`, NATS `4222`/`8222`. Consumers (`dashboard-updater`, `field-notifier`,
`test-consumer`) sengaja **tidak** expose host port; test memeriksanya via
`docker inspect` health + `docker compose logs` (baris JSON slog yang membawa
`hazard_id`).

## Cara menjalankan

Dari `code/apps/aat`:

```sh
python tests/test-problem-4.py
python tests/test-problem-5.py
```

Tidak perlu `chmod +x`; kedua file langsung jalan dengan Python.
Keduanya otomatis `cd` ke parent dir-nya (`code/apps/aat`).
Masing-masing mencetak baris `[PASS]` / `[FAIL]` / `[INFO]` plus ringkasan,
dan exit nonzero jika ada check wajib yang gagal.

## Cara test-consumer di-start/stop (Problem 5, langkah E)

Test tidak pernah menyalakan NATS/Postgres kedua dan tidak pernah me-restart
producer maupun consumer lama untuk menambah consumer ketiga:

```sh
# start (hanya jika belum running):
docker compose -p aat-part1 --env-file .env \
  -f compose.yaml -f compose.consumer.yaml up -d --build --no-deps test-consumer
# inspect:
docker compose -p aat-part1 --env-file .env \
  -f compose.yaml -f compose.consumer.yaml ps
docker compose -p aat-part1 --env-file .env \
  -f compose.yaml -f compose.consumer.yaml logs --since 5m test-consumer | grep hazard_id
```

`--no-deps` + `-p aat-part1` yang sama menjaga network `services` dan broker
yang sudah ada. Default test membiarkan `test-consumer` tetap running
(`CLEANUP_TEST_CONSUMER=0`); set `CLEANUP_TEST_CONSUMER=1` untuk men-stop-nya
hanya jika test sendiri yang men-start-nya. Stream, KV bucket, dan durable
consumer tidak pernah dihapus.

Jika file kedua gagal join ke network utama (mis. salah `-p`, atau hanya
`-f compose.consumer.yaml` tanpa `-f compose.yaml` sehingga tercipta project
terpisah + NATS kedua), test gagal di langkah E dengan penyebab yang jelas.
Perbaikan minimal: selalu chain `-f compose.yaml -f compose.consumer.yaml`
dengan `-p` dan `--env-file` yang sama.

## Timeout yang bisa dikonfigurasi

| Var                                | Default     | Arti                                                           |
| ---------------------------------- | ----------- | -------------------------------------------------------------- |
| `PROJECT` / `COMPOSE_PROJECT_NAME` | `aat-part1` | harus sama dengan stack yang running                           |
| `ENV_FILE`                         | `.env`      | dotenv berisi ports/tokens                                     |
| `TIMEOUT_SECS`                     | `90`        | budget tunggu delivery/log                                     |
| `POLL_INTERVAL_SECS`               | `2`         | interval bounded-poll                                          |
| `CURL_TIMEOUT_SECS`                | `10`        | timeout per request                                            |
| `REBUILD_TIMEOUT_SECS` (P4)        | `300`       | tunggu health setelah rebuild                                  |
| `REBUILD_SERVICE` (P4)             | `pvmbg`     | hanya service ini yang di-rebuild (`postgres`/`nats`/`aggregator` ditolak) |
| `DOWNTIME_EVENTS` (P5)             | `3`         | jumlah event yang dipublish saat dashboard stopped             |
| `CLEANUP_TEST_CONSUMER` (P5)       | `0`         | `1` = stop test-consumer jika test yang men-start-nya          |

Contoh (bash):

```sh
TIMEOUT_SECS=120 POLL_INTERVAL_SECS=1 python tests/test-problem-5.py
REBUILD_SERVICE=bmkg python tests/test-problem-4.py
```

Contoh (PowerShell Windows):

```powershell
$env:TIMEOUT_SECS="120"; python tests/test-problem-5.py
$env:REBUILD_SERVICE="bmkg"; python tests/test-problem-4.py
```

## Cara test events dibuat dan dibersihkan

- ID unik: `p4-…` / `p5-…` + timestamp UTC + PID + random
  (mis. `report_id=p5-c-20261009T000000Z-123-4567`), sehingga rerun tidak
  pernah tabrakan.
- Mekanisme: workflow asli `POST /internal/ingest/pvmbg`
  (`{"volcanic_reports":[{report_id, volcano_id:MERAPI, alert_level:Siaga, …}]}`)
  dengan `Authorization: Bearer $AGGREGATOR_TOKEN`, lalu
  `GET /internal/hazards?source=PVMBG&limit=1000` untuk konfirmasi mapping/storage,
  lalu polling `docker compose logs --since <ts>` untuk `hazard_id` di
  `aggregator` (`hazard published`), `dashboard-updater`, `field-notifier`,
  `test-consumer`. Stream NATS dicek via `GET /jsz?streams=true`
  (`HAZARDS_STREAM` / `hazards.created.v1` dari `internal/eventbus/jetstream.go`).
- Cleanup: baris hazard uji **dibiarkan** di Postgres (menghapusnya berarti
  merusak app data); test tidak pernah menjalankan `down -v`, tidak truncate
  tabel, tidak menghapus stream/KV/durables, tidak me-restart broker/producer
  selama failure test, dan tidak me-rebuild service yang tidak terkait.

## Apa yang dibuktikan tiap test (dan batasnya)

**Problem 4**

- A: container terpisah via `ps -q` + `docker inspect` Running state; mencetak
  service → 12-char container ID. Batas: membuktikan separasi container, bukan
  isolasi CPU.
- B: `up -d --build --no-deps pvmbg` (default; `bmkg` boleh), `GET /seismic-events`
  yang unrelated sebelum/sesudah, ID unrelated tidak berubah, service yang
  di-rebuild healthy. Batas: `pvmbg` sempat unavailable sesaat saat rebuild
  adalah hal yang wajar.
- C: ingest tanpa `confidence_level`, ingest dengan `0.85`, keduanya terbaca via
  `/internal/hazards`, `attributes.confidence_level==0.85` pada record baru,
  record lama tidak punya key tersebut (fleksibilitas = `attributes JSONB`, lihat
  `aggregator/internal/model/hazard_event.go` + `mapper/volcanic.go`). Dilaporkan
  gagal (bukan migrasi) jika tidak didukung.
- D: isolasi intended dari `docker compose config` (postgres ∈ {storage} saja,
  aggregator ∈ {services,storage}, lainnya ∉ storage, postgres tanpa host ports,
  `DATABASE_URL` hanya di aggregator) **plus** bukti runtime via
  `docker inspect` networks dan DNS probes (`field-notifier` tidak boleh resolve
  `postgres`; `aggregator` harus bisa). Grep kode hanya koroborasi. Akses host
  `localhost:808x` dibedakan eksplisit dari isolasi container-network.

**Problem 5**

- A/B: file dicetak, `nats` + kedua consumer running, `8222/healthz` +
  `jsz` menunjukkan `HAZARDS_STREAM`, subject/stream diambil dari kode, tidak ada
  nama consumer yang di-hardcode di `aggregator/`, async outbox relay (`publisher.go`).
- C: `hazard_id` yang sama di log kedua consumer (bounded poll, bukan fixed sleep).
  Container yang sekadar healthy tidak pernah diterima sebagai bukti delivery.
- D: hanya `stop dashboard-updater`; NATS/aggregator/field-notifier tetap jalan;
  N event downtime tetap terpublish + diterima field-notifier (tidak terblokir);
  setelah `start`, backlog **pasti** ter-redeliver — sesuai durable `DeliverAll` +
  `AckExplicit` + retensi `LimitsPolicy` 7 hari (`worker.go`). Jika redelivery
  gagal, test melaporkannya akurat sebagai loss (Core NATS ephemeral memang akan
  loss; stack ini durable JetStream, jadi loss = failure nyata).
- E: lifecycle third consumer seperti di atas; durables yang berbeda
  (`dashboard-updater`/`field-notifier`/`test-consumer`) membuktikan fan-out
  independen, bukan satu work queue bersama; ID aggregator + `git diff`
  membuktikan tidak ada perubahan producer.
- F: at-least-once (JetStream + ack + redelivery `AckWait 30s` + outbox
  `MsgID`/duplicates window) dengan dedupe KV `deliveryKey` di consumer
  (`worker.go` + `worker_test.go`); duplicate ingest mengembalikan `changed:0`.
  Exactly-once secara eksplisit **tidak** diklaim (crash di antara log handler
  dan KV write bisa menduplikasi baris log demo).

## Jika test gagal

- `docker daemon not reachable` → nyalakan Docker Desktop dulu.
- `has no container` / `not running` → nyalakan stack (lihat di atas) dengan
  `-p`/`-f`/`--env-file` yang sama.
- `did NOT receive <hazard_id>` → cek `docker compose logs <svc>` dan baris
  `hazard published` di `aggregator`; kemungkinan: poller/outbox lag
  (naikkan `TIMEOUT_SECS`), stream NATS hilang, atau salah `-p` (broker kedua).
- `container changed` (P4-B/P5-E) → ada yang me-restart service di luar test;
  rerun di stack yang idle.
- `DATABASE_URL appears N times` / `on storage network` (P4-D) → networks/env
  compose diubah; kembalikan intent `compose.yaml` (postgres internal).
- `test-consumer missed` → verifikasi `-p` yang sama, `NATS_URL=nats://nats:4222`,
  network `services`, dan nama durable yang berbeda.
- Setiap `[FAIL]` → exit 1; baris `[INFO]` hanya diagnostik, bukan vonis. Test
  yang tidak bisa jalan (mis. daemon mati) dilaporkan sebagai `[FAIL]` beserta
  prasyaratnya, tidak pernah diam-diam dianggap passed.
