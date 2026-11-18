# AAT — data, penyimpanan, dan akses client

Go + Gin, PostgreSQL, dan GORM. Tersedia: mock BMKG/PVMBG, pemetaan HazardEvent,
penyimpanan, polling otomatis beserta pencatatan kesehatan sumber, API internal
Aggregator, Auth Service, Client-Facing API, NATS JetStream, dashboard updater,
dan field notifier. Load testing sustained M1 tersedia di
[panduan load testing](docs/load-testing.md). Test per problem tersedia di
[panduan integration tests](tests/README.md), termasuk `test-problem-2.py` dan
`test-problem-3.py`. Laporan PDF M1 belum dibuat; audit implementasi tidak menggantikan laporan pengumpulan.

Petunjuk demo tersedia di [demo data](docs/demo.md),
[demo akses client](docs/demo-client.md), dan [demo event](docs/demo-events.md).

## Alur data

Alur yang diwajibkan spesifikasi (panah menunjukkan pemanggil → tujuan):

```text
Aggregator → polling BMKG / PVMBG → normalisasi → PostgreSQL
Client → Client-Facing API → API Aggregator → PostgreSQL
```

Mock bersifat pasif; tidak membaca atau mengirim data ke Aggregator. Aggregator
melakukan polling independen pada interval konfigurabel; ingest HTTP internal tetap
tersedia untuk demo data manual. Hanya Aggregator yang mengakses DB;
PostgreSQL berada di network internal tanpa port host.

## Menjalankan

Prasyarat:

- Docker Engine aktif dalam mode Linux containers dan Docker Compose v2.
- Python 3 untuk script konfigurasi dan integration test; jika command di mesin
  Anda adalah `python`, ganti `python3` pada contoh berikut dengan `python`.
- Go 1.25+ hanya untuk menjalankan unit test atau service langsung di host;
  build container sudah menyediakan Go melalui Dockerfile.
- k6 untuk load test Problem 2. Demo manual berbasis shell memakai Bash,
  `curl`, dan `jq`; integration test Python tidak membutuhkan `jq`.

Jalankan dari root repository:

```sh
cd code/apps/aat
# Buat .env dari contoh dan isi delapan secret acak; secret nyata tidak dirotasi.
python3 scripts/setup-env.py
# Satu perintah menyalakan seluruh komponen M1.
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml up -d --build
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml ps
```

Script membuat `.env` dari [.env.example](.env.example), menghasilkan delapan
secret berbeda, dan mengisi `START_TIME`. Kredensial dan timestamp yang sudah
ada dipertahankan. `.env` tidak ikut di-commit; semua placeholder secret pada
`.env.example` harus diganti sebelum service dijalankan.

Tunggu health check dan polling pertama selesai sebelum pengujian; periksa
`ps` dan `docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml logs --tail 30 aggregator`.
Setiap service aplikasi menyediakan `GET /health` tanpa autentikasi;
NATS menyediakan `/healthz` pada port monitoring `8222`.

### Port dan perubahan konfigurasi

| Service | Port host default | Variabel `.env` |
| --- | --- | --- |
| BMKG | `127.0.0.1:8081` | `BMKG_PORT` |
| PVMBG | `127.0.0.1:8082` | `PVMBG_PORT` |
| BNPB Client-Facing API | `127.0.0.1:8080` | `CLIENT_PORT` |
| Aggregator | `127.0.0.1:8083` | `AGGREGATOR_PORT` |
| Auth Service | `127.0.0.1:8084` | `AUTH_PORT` |
| NATS / monitoring | `127.0.0.1:4222` / `127.0.0.1:8222` | Mapping tetap pada `compose.yaml` |

PostgreSQL dan consumer tidak mempublikasikan port host. Untuk mengganti port,
edit variabel terkait di `.env`, misalnya `CLIENT_PORT=8090`, lalu jalankan ulang:

```sh
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml up -d
```

Client API kemudian diakses melalui `http://localhost:8090`. Port container dan
URL antarservice tetap sama; variabel `*_PORT` di atas hanya mengubah port host.
Perubahan environment lain juga membutuhkan pembuatan ulang container melalui
`up -d`; mengedit `.env` saja belum mengubah container yang sedang berjalan.

### Menjalankan bagian 2

Setelah `.env` bagian 1 diisi, tambahkan kredensial client dan jalankan overlay:

```sh
python3 scripts/setup-client-env.py
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml up -d --build
```

Dengan port default, Auth Service tersedia di `http://localhost:8084`, Client API di
`http://localhost:8080`. Endpoint data client adalah `GET /hazards` dengan Bearer
access token. Identitas demo: `public` (Media, tujuh field Ringkasan), `responder` (Tim Lapangan,
seluruh field), dan `analyst` (BNPB Internal, seluruh field). Default TTL access
token 60 detik. Permintaan field Mentah oleh Media menghasilkan 403. Ikuti **[demo-client](docs/demo-client.md)**
untuk login, refresh, pengujian mock, dan perintah operasional.

Client API menyajikan `sources[].status` dan `sources[].stale`, berdasarkan
`/internal/source-status` milik Aggregator.
[Kontrak integrasi](docs/client-contract.md) mendefinisikan format dan pembagian
tanggung jawabnya. Pembatasan concurrency berlaku per proses, dengan penolakan
429 saat semua slot terpakai.

## Variabel konfigurasi

Variabel berikut tersedia dalam `.env.example` dan dibaca Compose dari `.env`.
Nilai secret contoh bukan kredensial untuk menjalankan service.

| Konfigurasi                                         | Default / ketentuan                                                  |
| --------------------------------------------------- | -------------------------------------------------------------------- |
| `BMKG_API_KEY`, `PVMBG_TOKEN`, `AGGREGATOR_TOKEN`   | Wajib, berbeda; token Aggregator hanya untuk internal                |
| `POSTGRES_USER`, `POSTGRES_DB`, `POSTGRES_PASSWORD` | Wajib; gunakan password hex agar aman untuk format connection string |
| `BMKG_DELAY_MS`                                     | `100`, rentang 50–150 ms                                             |
| `PVMBG_DELAY_MIN_MS`, `PVMBG_DELAY_MAX_MS`          | `500`, `3000`; rentang 0–10000 ms                                    |
| `EVENT_INTERVAL_SECONDS`                            | `10`, rentang 1–10 detik                                             |
| `START_TIME`                                        | Wajib, RFC3339; dibuat sekali oleh `scripts/setup-env.py` dan dipakai kedua mock |
| `DB_MAX_CONNS`                                      | `5` per Aggregator                                                   |
| `POLL_INTERVAL_SECONDS`, `POLL_TIMEOUT_MS`          | `3`, `4000`; interval dan batas waktu polling Aggregator             |
| `NATS_URL`                                          | `nats://nats:4222`; alamat broker untuk Aggregator dan consumers     |
| `BMKG_PORT`, `PVMBG_PORT`, `AGGREGATOR_PORT` | `8081`, `8082`, `8083`; port host API sumber dan Aggregator |
| `AUTH_PORT`, `CLIENT_PORT` | `8084`, `8080`; port host Auth dan BNPB Client-Facing API |
| `AUTH_INTERNAL_TOKEN` | Wajib; secret introspeksi Client API → Auth, berbeda dari kredensial sumber dan Aggregator |
| `PUBLIC_CLIENT_SECRET` | Wajib; kredensial Media dengan identitas `public` |
| `RESPONDER_CLIENT_SECRET` | Wajib; kredensial Tim Lapangan dengan identitas `responder` |
| `ANALYST_CLIENT_SECRET` | Wajib; kredensial BNPB Internal dengan identitas `analyst` |
| `ACCESS_TTL_SECONDS` | `60`; masa berlaku access token dalam detik |
| `REFRESH_TTL_SECONDS` | `3600`; masa berlaku sesi refresh dalam detik, minimal sama dengan TTL access |
| `AUTH_MAX_SESSIONS` | `1000`; batas sesi Auth dalam memori |
| `AUTH_MAX_CONCURRENT` | `32`; batas request bersamaan per proses Auth |
| `CLIENT_MAX_CONCURRENT` | `16`; batas request bersamaan per proses Client API |
| `UPSTREAM_TIMEOUT_MS` | `3000`; batas waktu total request upstream dalam satu request Client API |
| `STALE_AFTER_SECONDS` | `60`; ambang umur polling sukses terakhir untuk penanda stale |

Secret Auth internal dan ketiga client minimal 32 karakter serta harus berbeda.
`scripts/setup-env.py` menyiapkannya secara otomatis. Jika service dijalankan
langsung di host, konfigurasi tambahan adalah `DATABASE_URL` untuk Aggregator,
`BMKG_URL`, `PVMBG_URL`, `AUTH_URL`, dan `AGGREGATOR_URL` untuk alamat upstream.
Di Compose, nilai-nilai tersebut sudah disediakan melalui environment service;
mengubah port host tidak perlu mengubah URL internal.

Mock memiliki 20 seed record hingga `START_TIME` (dibulatkan ke bawah sesuai interval),
lalu menghasilkan satu record per interval setelahnya hingga waktu saat ini. Pertahankan `START_TIME` dan
`EVENT_INTERVAL_SECONDS` agar timeline dan record tetap sama setelah restart.
Filter `since` inklusif; warning mengikuti waktu gempa. Toggle outage dan schema PVMBG
tetap reset saat restart. Untuk `.env` lama, jalankan kembali `python3 scripts/setup-env.py`
sebelum rebuild; timestamp yang sudah ada dan kredensial tidak diubah.

## Endpoint

| Service / autentikasi                  | Endpoint                                                                             |
| -------------------------------------- | ------------------------------------------------------------------------------------ |
| BMKG — `X-BMKG-Key`                    | `GET /seismic-events`, `GET /tsunami-warnings`; keduanya menerima `?since=`          |
| PVMBG — Bearer `PVMBG_TOKEN`           | `GET /volcanic-reports?since=`, `POST /admin/schema-version`, `POST /admin/outage`   |
| Aggregator — Bearer `AGGREGATOR_TOKEN` | `POST /internal/ingest/bmkg`, `POST /internal/ingest/pvmbg`, `GET /internal/hazards` |
| Auth — kredensial client / refresh token | `POST /auth/token`, `POST /auth/refresh` |
| Auth internal — Bearer `AUTH_INTERNAL_TOKEN` | `POST /internal/introspect` |
| Client API — Bearer access token | `GET /hazards` |

Body admin: `{"enabled":true|false}`. Body ingest BMKG:
`{"seismic_events":[...],"tsunami_warnings":[...]}`; PVMBG: `{"volcanic_reports":[...]}`.
Respons ingest: `{"changed":N,"items":[...]}`; payload identik menghasilkan `changed:0`.

Aggregator polling memakai `BMKG_URL` dan `PVMBG_URL` (default service Compose),
menjalankan kedua sumber secara independen, dan memakai `POLL_INTERVAL_SECONDS`
serta `POLL_TIMEOUT_MS`. `GET /internal/source-status` mengembalikan hasil polling
terakhir untuk BMKG dan PVMBG; kegagalan source tidak menghentikan polling source lain.

Pembacaan hazards menerima `source`, `limit` (1–1000), dan `after`; respons berisi
`items` dan `next_after`. Pagination berdasarkan ID, bukan cursor perubahan.
Body maksimal 2 MiB; input tidak valid → 400, auth salah → 401, DB gagal → 503.
Batch gagal di-rollback. Log JSON menyertakan `X-Correlation-ID`.

Contoh ingest, perubahan skema, outage, dan auth silang: **[panduan demo](docs/demo.md)**.
Untuk verifikasi otomatis bagian 1 melalui HTTP (manual ingest, tanpa worker polling):

```sh
python3 scripts/demo-data.py
python3 scripts/demo-client.py --wait-expiry
```

Script kedua menunggu kedaluwarsa alami token Tim Lapangan sesuai TTL sebelum refresh.
Keduanya menerima `--env-file PATH` untuk konfigurasi stack tes terisolasi.

## Struktur dan penyimpanan

- `bmkg/`, `pvmbg/`: masing-masing memiliki `cmd/server`, `internal/mock`, dan Dockerfile.
  Di dalam mock, `model/` menangani data/state, `controller/` menangani HTTP,
  dan `routes.go` mendaftarkan endpoint.
- `aggregator/`: handler HTTP dan operasi GORM di `internal/controller`, model `HazardEvent` dan AutoMigrate di `internal/model`, pemetaan di `internal/mapper`; startup dan polling di `cmd/server`.
- `auth/`: identitas client, token opaque, rotasi refresh, dan introspeksi internal.
- `client/`: API client, allowlist field, timeout upstream, dan penyajian stale.
- `internal/httpkit/`: helper transport/config/log bersama; business logic tetap per service.

Field standar HazardEvent memakai kolom bertipe; atribut dinamis memakai `attributes`
JSONB. Payload sumber disimpan sebagai JSONB untuk korelasi/pemetaan ulang.
Field tambahan diteruskan ke attributes tanpa migrasi; perubahan tipe atau penghapusan
field wajib ditolak. Warning mengoverride severity gempa; ID tetap stabil.

[AutoMigrate](aggregator/internal/model/migrate.go) membuat atau memperbarui tabel,
kolom, constraint, dan index dari definisi model saat startup. Migrasi berjalan di dalam
transaksi dengan advisory lock untuk menyelaraskan startup replica. Format lama yang
menyimpan seluruh HazardEvent di kolom `document` tidak lagi dikonversi otomatis;
data tersebut perlu dipindahkan ke kolom bertipe sebelum memakai versi ini.

PostgreSQL dipilih untuk constraint dan transaksi plus JSONB; SQLite membatasi replikasi
berbasis file, sedangkan MongoDB menambah model operasional berbeda.

## Skenario pengujian P1–P5

Jalankan satu per satu dari `code/apps/aat` pada stack yang sudah aktif agar
perubahan schema, outage, dan restart antartest tidak saling mengganggu.

| Problem | Cara memicu | Yang diperiksa |
| --- | --- | --- |
| P1 — interoperabilitas | `python3 tests/test-problem-1.py`; tekan Enter saat diminta | Mapping kedua sumber; toggle schema PVMBG tanpa restart; `confidence_level` masuk `attributes` |
| P2 — concurrency dan availability | Atur delay PVMBG 3 detik seperti contoh berikut, lalu `python3 tests/test-problem-2.py` | BMKG-only p95 <300 ms; load ≥50 VU selama ≥60 detik; metrik throughput/latensi/error/429; outage, stale, dan pemulihan |
| P3 — autentikasi | `python3 tests/test-problem-3.py` | Kredensial silang ditolak; field Media dibatasi; access token kedaluwarsa alami, refresh, dan penolakan token lama |
| P4 — service dan storage | `python3 tests/test-problem-4.py` | Stop/rebuild PVMBG tanpa restart service lain; atribut dinamis tanpa migrasi; isolasi Canonical Store |
| P5 — pub-sub | `python3 tests/test-problem-5.py` | Dua consumer independen; stop/resume consumer dan backlog; container subscriber ketiga tanpa perubahan producer; deduplikasi |

Persiapan P2: ubah dua nilai berikut di `.env`, lalu buat ulang hanya PVMBG:

```dotenv
PVMBG_DELAY_MIN_MS=3000
PVMBG_DELAY_MAX_MS=3000
```

```sh
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml up -d --no-deps pvmbg
python3 tests/test-problem-2.py --vus 50 --seconds 65
```

Pertahankan TTL access default 60 detik untuk demo P3. P4 menghentikan dan
me-rebuild PVMBG; P5 menghentikan sementara dashboard updater dan menambahkan
`test-consumer` melalui `compose.consumer.yaml` dengan project yang sama.
Secara default P5 membiarkan container tambahan tersebut berjalan.

Detail prasyarat dan hasil ada di [panduan integration tests](tests/README.md).
Demo manual tersedia untuk [schema/outage/kredensial silang](docs/demo.md),
[login/field/refresh](docs/demo-client.md), dan
[stop/resume/penambahan subscriber](docs/demo-events.md).
Untuk mengulang load test saja, ikuti [panduan k6](docs/load-testing.md).

## Tes dan operasional

Memerlukan Go 1.25+. Dari direktori AAT:

```sh
go test ./...
go vet ./...
# Opsional: PostgreSQL khusus tes, setiap kasus memakai schema terpisah.
AAT_TEST_DATABASE_URL='postgres://user:password@localhost/testdb?sslmode=disable' \
  go test ./aggregator/internal/controller -run TestAutoMigrateAndIngestion -v

# Rebuild satu service tanpa restart service lain.
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml up -d --build --no-deps pvmbg
# Hentikan stack; data PostgreSQL tetap tersimpan.
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml down
```

Untuk menjalankan di host: `go run ./<service>/cmd/server` setelah mengekspor env.
Aggregator membutuhkan `DATABASE_URL` yang terjangkau; jangan bentrok dengan port Compose.
Compose biasa cukup untuk satu Aggregator. `compose.scale.yaml` opsional untuk replica
lokal, menghapus akses host Aggregator, dan belum menyediakan reverse proxy.

Tes bagian 2 mencakup field sesuai identitas, refresh/replay, concurrency, dan
penyajian stale dengan upstream simulasi. Polling otomatis, outbox, dan JetStream
delivery kini tersedia. Load test terintegrasi memakai k6, 50 VU selama 65 detik;
lihat [metode dan hasil](docs/load-testing.md).

Untuk demonstrasi pub-sub, penghentian dan pemulihan consumer, serta penambahan
subscriber baru, ikuti [panduan Problem 5](docs/demo-events.md).

## Deployment Kubernetes lokal — M2

Deployment memakai [cluster lokal](../../infrastructure/README.md) dengan dua VM berkernel terpisah. Registrasi tujuh service (Aggregator, Auth, Client API, BMKG, PVMBG, dashboard updater, field notifier), runtime values, dan image digest berada di `../../infrastructure/argo-apps/dev/aat/`. Image baseline digunakan ulang dari `infrastructure-main.zip`. Workflow
[GitHub Actions AAT](../../../.github/workflows/aat-ci.yml) membuild service yang
berubah di `main`, publish ke GHCR, lalu memperbarui digest GitOps agar Argo CD
melakukan rollout. Tidak ada job test atau build pada PR. Build manual tersedia
melalui Actions → AAT — build and deploy → Run workflow pada `main`.

Helper CI berada di `.github/scripts/aat/`, dengan nama sesuai tugasnya:

| File | Tanggung jawab |
| --- | --- |
| `service_build_config.py` | Daftar service, folder source, dan dependensi build bersama |
| `detect_changed_services.py` | Memilih service yang perlu dibuild dari perubahan Git |
| `save_published_image_digest.py` | Menyimpan digest hasil publish sebagai artifact JSON |
| `update_gitops_image_pins.py` | Memvalidasi artifact dan memperbarui pin image GitOps |
| `commit_and_push_image_pins.sh` | Commit/push pin ke main dengan autentikasi sementara dan retry |

CI hanya menggunakan `GITHUB_TOKEN` sementara dengan izin per job; tidak memakai
PAT, kubeconfig, atau Secret aplikasi. Image GHCR harus public agar VM bisa pull
secara anonim; pin tidak diubah jika akses tersebut gagal. Izinkan workflow
memakai `packages: write` dan `contents: write` sesuai job. Push digest memakai
push biasa ke `main`, jadi aturan branch protection tetap berlaku.

Semua action dipin ke commit penuh, credential Git tidak dipersist di checkout,
dan credential Docker dibersihkan sebelum helper metadata berjalan. Perubahan
workflow, helper CI, serta Dockerfile yang masuk `main` tetap harus direview:
orang yang bisa mengubah workflow di main juga bisa mengubah batas akses token.
Konfigurasi workflow tidak memberi jaminan absolut terhadap kompromi maintainer
atau action yang sudah dipercaya.

Workload berada pada namespace `sistem-informasi-kebencanaan-bnpb`. PostgreSQL dan NATS JetStream dipasang melalui `charts/aat-dependencies` di infrastructure, dengan volume `local-path`. NetworkPolicy membatasi PostgreSQL ke Aggregator dan NATS ke Aggregator/consumer.

| Role pod | Request / limit memory |
| --- | --- |
| aggregator | 256Mi / 512Mi |
| store / broker | 512Mi / 1Gi |
| api / consumer / other | 128Mi / 256Mi |
| mock | 64Mi / 128Mi |

Aggregator default tiga replica yang tersebar 2+1 pada dua node menurut hostname, rolling update `maxUnavailable: 0`, dan PDB `minAvailable: 2`. Auth tetap satu replica karena sesi image baseline berada dalam memori proses. HPA Aggregator opsional 3–5 replica: tambahkan `../../argo-apps/dev/aat/aggregator/autoscaling.yaml` ke `spec.source.helm.valueFiles` pada Application Aggregator, lalu commit/push. Lepaskan overlay tersebut sebelum pengujian scale manual.

Dari `code/infrastructure`, setelah prasyarat cluster disiapkan:

```bash
task up
task logs APP=aggregator
task scale REPLICAS=5
task scale REPLICAS=2
task scale REPLICAS=3
```

Client API tersedia di `http://192.168.56.10:30080`, Auth di `http://192.168.56.10:30084`. Untuk akses Aggregator dari host:

```bash
KUBECONFIG=$PWD/.state/kubeconfig kubectl -n sistem-informasi-kebencanaan-bnpb port-forward svc/aggregator 8083:8083
```

Bootstrap menyimpan kredensial pada `.env.runtime` (permission 0600), membuat Secret `aat-runtime`, serta mempertahankan `START_TIME` mock melalui ConfigMap `aat-clock` dan `.state/mock-start-time`. Nilai dipertahankan saat deploy ulang dan dapat dipulihkan dari cluster. Token asisten untuk integrasi lanjutan tercantum pada `.env.example` infrastructure dan perlu diisi sesuai sesi tugas.

Dashboard Grafana **AAT — layanan dan runtime lokal** menampilkan throughput, rasio 5xx, dan p95 dari log request JSON (`msg=request`, `service`, `status`, `latency_ms`), serta CPU/memori/replica dari cAdvisor dan kube-state-metrics. Cari `correlation_id` melalui Loki. Kode Go menyediakan SDK OTel dengan span HTTP server/client, PostgreSQL, polling, serta propagasi context outbox/NATS. Image yang dipakai harus dibangun dari kode berinstrumentasi; environment `OTEL_*` saja tidak menginstrumentasi image baseline lama.

Image baseline dan konfigurasi cluster belum membuktikan kelulusan P1–P5 M2. Sharding/replikasi storage, koordinasi leader/fencing, Feed Satelit, Audit API, serta bukti load/failure/recovery masih membutuhkan implementasi dan pengujian aplikasi. Checklist lokal ada di `task.local.md`.

Tester Agent memakai manifest pada commit yang dirujuk PDF dan image aslinya. Jalankan dari `code/infrastructure`:

```bash
task tester:install
task tester:list
task tester:practice SCENARIO=nama-dari-list
task vm:suspend-worker NODE=aat-worker-1
task vm:resume-worker NODE=aat-worker-1
```

Gunakan worker untuk gangguan awal karena cluster hanya mempunyai satu control plane. Skenario ujian memerlukan token/kunci asisten; catat log Tester Agent serta bukti kernel terpisah dan spesifikasi host untuk laporan.

Sensor Pantai belum dideploy karena contoh manifest pada PDF lokal kosong. Setelah kontraknya tersedia, gunakan image `sustuna/sensor-pantai@sha256:51fbd90d3891ad6e9f5768ad330ffb65205644cb29e50da65c7538f2e0b4397d`, namespace aplikasi, label `role=sensor`, dan memory 128Mi/256Mi.


### Fondasi storage dan Audit API M2

Kode Aggregator menyediakan adapter storage PostgreSQL serta endpoint audit
`events`, `checksum`, dan `location`. Gunakan `AUDIT_TOKEN` khusus audit;
tanpa token ini akses audit dinonaktifkan. `STORE_NODE_NAME` adalah node database,
bukan node Aggregator; `STORE_UNIT` default `postgres`. Backend masih satu
PostgreSQL; deployment infra perlu meneruskan Secret dan identitas node storage.


### Distributed tracing

Spek M2 bagian Observabilitas mewajibkan trace lintas seluruh service yang dilalui
satu alur, dengan durasi tiap langkah. Infra menyediakan collector OTLP dan Tempo;
pemilik masing-masing aplikasi bertanggung jawab meneruskan context pada jalurnya.

SDK Go memakai OTLP HTTP/protobuf melalui `OTEL_EXPORTER_OTLP_ENDPOINT`; tanpa
endpoint, export dinonaktifkan untuk pengembangan lokal. Manifest AAT sudah
memuat endpoint collector dan service.name. Rebuild dan rollout image diperlukan
agar kode instrumentasi baru berjalan di cluster. Compose juga meneruskan endpoint
opsional dari .env; alamatnya harus dapat dijangkau dari container.

HTTP memakai W3C traceparent/tracestate. Respons memuat X-Trace-ID dan log request
memuat trace_id selain correlation_id. Query PostgreSQL memakai nama ringkas dan `db.query.text` tersanitasi; literal,
komentar, parameter values, token, dan payload tidak diekspor. `db.namespace`,
server, operation, collection dari metadata GORM, jumlah baris SELECT, serta
SQLSTATE error ditambahkan bila tersedia. Query besar atau konstruksi yang tidak
didukung tetap generik. Query ingest dikelompokkan pada span `ingest <source>`. Context ingest disimpan bersama outbox sehingga
publish dan consumer NATS dapat melanjutkan trace sesudah restart. HTTP health
check consumer saja belum membuktikan alur pesan; uji jalur asynchronous secara
terpisah sebelum mencentang bukti demo.

Default sampling parentbased_always_on cocok untuk demo. Untuk load test gunakan
OTEL_TRACES_SAMPLER=parentbased_traceidratio dan OTEL_TRACES_SAMPLER_ARG misalnya
0.1; anak span mengikuti keputusan sampling parent. Export berjalan melalui
antrean batch terbatas dan tidak menunggu collector pada request aplikasi.
SIGTERM/SIGINT menguras HTTP request lalu melakukan flush exporter dengan timeout.

Untuk bukti, kirim GET /hazards dengan token client, catat X-Trace-ID, lalu cari ID
tersebut pada datasource Tempo di Grafana Explore. Periksa Client API, Auth,
Aggregator, dan child span PostgreSQL pada trace yang sama beserta durasinya.

## Milestone 2: Citus storage

P1 menetapkan hasil (distribusi, replikasi, kehilangan disk, pemulihan, dan audit),
tanpa mewajibkan mesin sharding buatan sendiri. Implementasi memakai Citus 13.0.1
pada PostgreSQL 17 dan Patroni 4.1.0. Routing shard, streaming replication,
promosi standby, dan pemulihan replika dikelola database/Patroni.

| VM | Kelompok data yang disimpan |
| --- | --- |
| `aat-control` | coordinator metadata, anggota grup 1 dan 3 |
| `aat-worker-1` | anggota grup 1 dan 2 |
| `aat-worker-2` | anggota grup 2 dan 3 |

Setiap grup worker memiliki dua instance di disk node berbeda. Primary dipilih
Patroni, bukan ditentukan oleh nama VM. Tidak ada VM menyimpan ketiga grup data.
Ini **dua salinan melalui PostgreSQL streaming replication**, bukan pengaturan
`citus.shard_replication_factor=2`. Jangan menjadikan `hazard_events` reference
table karena reference table disalin penuh ke setiap worker.

`hazard_events` dibagi menjadi 24 hash shard berdasarkan `source_ref_id` yang
berkardinalitas tinggi. Lokasi geografis tidak menjadi distribution key, sehingga
lonjakan satu wilayah tetap tersebar. Unique key `(source, source_ref_id)` tetap
mencegah duplikasi. Primary key menyertakan distribution key; pembaruan tetap
mempertahankan `hazard_id` pertama. Source state, polling fence, dan outbox berada
pada coordinator dan di-commit bersama perubahan canonical melalui transaksi
Citus. Payload outbox dibersihkan setelah publikasi berhasil agar coordinator
tidak menjadi arsip seluruh HazardEvent. NATS tetap memiliki retensi event sendiri.

Deploy Kubernetes dari root `code/infrastructure` dengan `task up`. Image Citus +
Patroni dibangun oleh pipeline AAT dan dipasang melalui pin image pada Argo CD.
Pada merge pertama, tunggu pipeline menerbitkan image dan memperbarui digest
sebelum menilai readiness. Compose M1 tetap memakai PostgreSQL tunggal untuk
pengembangan; Compose bukan bukti pemenuhan P1/P2.

Konfigurasi Aggregator tambahan:

| Variabel | Nilai Kubernetes |
| --- | --- |
| `STORE_BACKEND` | `citus`; kosong/`postgres` untuk development M1 |
| `CITUS_MEMBERS_FILE` | `/etc/citus/members.json`, inventory dari ConfigMap storage |
| `AUDIT_TOKEN` | Secret tersendiri, berbeda dari token aplikasi |
| `POSTGRES_REPLICATION_PASSWORD` | Secret khusus koneksi replikasi, dibuat bootstrap |

API biasa mendukung `GET /hazards?order=recent&limit=50` dan `cursor` dari
`next_cursor`, diurutkan global menurut `occurred_at DESC, hazard_id DESC`.
Pagination audit tetap `ingested_at ASC, hazard_id ASC`. Citus melakukan sort
lintas shard; cursor tidak mengandung alamat worker sehingga tetap berlaku
setelah failover/rebalance. Jangan menggabungkan `after` dengan `order=recent`.
Read memakai isolasi PostgreSQL/Citus READ COMMITTED; pagination merupakan view
hidup, bukan snapshot lintas beberapa request. Update yang berlangsung saat
pagination dapat mengubah keanggotaan halaman. Untuk pembandingan checksum demo,
gunakan dataset stabil dan catat waktu pengukuran. Write canonical, source cursor,
dan outbox tetap berada dalam satu transaksi; kegagalan transaksi tidak dianggap
sebagai ingest berhasil.

Selama worker belum tersedia, query dibatasi waktu dan mengembalikan HTTP 200,
`partial:true`, serta `unavailable_units`; checksum bernilai null. Bila query SQL
lintas shard gagal, halaman konservatifnya kosong dengan cursor retry yang sama.
Halaman tersebut tidak mengklaim akhir dataset. Setelah promosi selesai, query
kembali lengkap. Audit location menggunakan katalog shard Citus dan status hidup
Patroni; standby yang masih mengejar WAL tidak diiklankan sebagai salinan lengkap.

Synchronous replication aktif dalam keadaan sehat. Dengan dua anggota per grup,
Patroni dapat melanjutkan write pada anggota yang tersisa setelah satu node hilang
(`synchronous_mode_strict:false`); grup itu sementara hanya memiliki satu salinan.
Jangan mengklaim tahan kegagalan kedua sebelum replica kembali streaming/sync.
Coordinator tunggal dipasang pada control plane: uji kehilangan node wajib memilih
worker non-control-plane. Desain ini belum menyediakan HA untuk control plane.

Pemulihan node yang sama dengan disk kosong: nyalakan kembali node, sediakan PVC
local-path kosong untuk unit yang hilang, lalu jalankan instance Patroni-nya.
Patroni melakukan base backup dari primary yang bertahan, kemudian mengejar WAL;
read/write ke primary tetap berjalan. Local PV yang masih menunjuk direktori
hilang tidak membuat direktori baru otomatis: setelah mengonfirmasi disk unit itu
sudah hilang dan replica lain sehat, hentikan unit terdampak, ganti PVC-nya dengan
PVC kosong, lalu nyalakan kembali. Jeda reconciliation Argo CD selama tindakan
manual tersebut agar jumlah replika tidak dikembalikan di tengah pemulihan.
Jangan menghapus PVC primary yang selamat atau memaksa promosi standby tertinggal.
Untuk node pengganti bernama lain, ubah placement grup pada Helm values serta
inventory, pertahankan unit yang selamat, dan tunggu anggota baru selesai cloning.

Untuk menambah kapasitas dengan grup worker baru, tambahkan sepasang anggota di
node berbeda pada `citus.groups` (hindari placement yang membuat satu node
memegang seluruh grup), tunggu Patroni mendaftarkan primary, kemudian jalankan
pada coordinator:

```sql
SELECT citus_rebalance_start();
SELECT * FROM citus_rebalance_status();
```

Citus Community menyediakan rebalance online menggunakan logical replication.
Menambah worker saja tidak memindahkan shard lama. Inventory ConfigMap harus
ikut diperbarui dan Aggregator di-rollout agar audit mengenali anggota baru.

### Verifikasi storage

Suite integrasi memakai PostgreSQL/Citus nyata. Jalankan terhadap database uji
terpisah yang sudah memiliki tiga worker group, bukan database demo:

```sh
AAT_TEST_CITUS=1 AAT_TEST_DATABASE_URL='postgres://...database-uji...' go test ./...
```

Untuk bukti demo P1, muat katalog asisten, rekam sebaran per node dan checksum,
matikan worker beserta disknya, periksa respons parsial selama failover, kemudian
cocokkan checksum lengkap. Pulihkan replika dari disk kosong dan ulangi kehilangan
worker berikutnya. Gunakan Tester Agent resmi pada tiga VM ber-kernel terpisah;
cluster container hanya untuk pengujian pengembangan. Dashboard analitik P1.7
tersedia melalui Client API seperti dijelaskan di bawah; bukti dataset resmi
dan skenario VM tetap perlu dikumpulkan.

Rujukan: [Citus cluster management](https://docs.citusdata.com/en/v13.0/admin_guide/cluster_management.html),
[Patroni Citus integration](https://patroni.readthedocs.io/en/rel_4_1/citus.html),
dan [jaminan synchronous replication](https://patroni.readthedocs.io/en/rel_4_1/replication_modes.html).

## Dashboard analitik P1

Buka **http://client.localhost/** setelah `task up`, lalu masuk sebagai BNPB
Internal menggunakan kunci akun `analyst`. Ambil kuncinya dari infrastructure:

```sh
cd code/infrastructure
task password APP=analyst
```

Untuk Compose, gunakan `http://localhost:8080/` dan `ANALYST_CLIENT_SECRET`
dari `.env`. Halaman ini dilayani langsung oleh Client API; tidak ada service atau
proses Node tambahan. Browser menyimpan token hanya di memori, memperbarui token
melalui Auth Service, dan menghapus sesi lokal saat keluar/halaman ditutup.
Token internal Aggregator/Audit tidak dikirim ke browser.

Dashboard menyediakan filter 24 jam, 7/30/90 hari, tanggal khusus, sumber data,
pencarian wilayah, serta ekspor CSV. Grafik mencakup tren per jenis bahaya,
jumlah per severity, dan jumlah per wilayah. Rentang mengikuti `occurred_at`
dengan batas **from inklusif, to eksklusif**. Waktu filter disesuaikan ke UTC;
bucket jam/hari/minggu/bulan/tahun menggunakan UTC. Grafik dibatasi 1000 bucket;
rentang historis panjang otomatis memakai interval bulan atau tahun.

Model canonical menyimpan warning tsunami pada event `SEISMIC`. Karena itu:

- **Gempa** menghitung seluruh event `SEISMIC`.
- **Tsunami** menghitung subset gempa dengan array `attributes.tsunami_warnings`
  yang tidak kosong; `potential_tsunami=true` saja tidak dihitung sebagai warning.
- **Gunung berapi**, **kebakaran hutan**, dan **pesisir** mengikuti `VOLCANIC`,
  `WILDFIRE`, dan `COASTAL`.
- Total, severity, dan wilayah menghitung setiap HazardEvent sekali. Jumlah kelima
  kategori tidak dijumlahkan sebagai total karena gempa/tsunami beririsan.

Endpoint `GET /internal/analytics` pada Aggregator menggunakan token aplikasi
internal; `GET /analytics` pada Client API hanya menerima access token `analyst`.
Parameter: `from`, `to` (RFC3339), `interval=hour|day|week|month|year`, dan `source` opsional.
Seluruh grafik dihitung dalam satu statement SQL dari canonical Citus, tanpa
salinan data terpisah pada consumer. Write baru terlihat pada refresh berikutnya;
setiap request tetap mengikuti isolasi READ COMMITTED yang didokumentasikan di atas.

Jika query lintas shard gagal, endpoint mengembalikan HTTP 200, `partial:true`,
`unavailable_units`, dan **`total:null`**. Nilai nol hanya digunakan untuk hasil
lengkap yang memang kosong. Dashboard menandai status parsial; bila ada snapshot
sebelumnya, grafiknya diberi label belum mutakhir dan ekspor dinonaktifkan.
Ketersediaan sumber ditampilkan terpisah: storage yang dapat dibaca tidak berarti
semua sumber sudah berhasil mengirim kejadian terbaru.

### Import katalog gempa

Tool import memakai kontrak **SeismicEvent BMKG** yang sudah ada: JSON array atau
NDJSON dengan `event_id`, `region_name`, `occurred_at` bertimezone,
`epicenter_lat`, `epicenter_lon`, `magnitude`, `depth_km`, dan `potential_tsunami`
(boolean). Field tambahan tetap diteruskan ke mapper. Bila format katalog asisten
berbeda, petakan ke kontrak tersebut sebelum import; tool tidak menebak nama kolom.

Jalankan dari `code/apps/aat`. Port-forward Aggregator pada terminal terpisah:

```sh
kubectl --kubeconfig ../../infrastructure/.state/kubeconfig \
  -n sistem-informasi-kebencanaan-bnpb port-forward service/aggregator 8083:8083
```

Validasi seluruh file dahulu, lalu import melalui jalur ingestion biasa:

```sh
python3 scripts/import-catalog.py /path/katalog.json --dry-run
python3 scripts/import-catalog.py /path/katalog.json \
  --env-file ../../infrastructure/.env.runtime --batch-size 100
```

Seluruh input divalidasi sebelum request pertama. Setiap batch menggunakan transaksi
endpoint ingestion; kegagalan di tengah file dapat dilanjutkan dengan menjalankan
ulang tool. Payload identik didedup dan identitas event tetap dipertahankan. Tool
menampilkan jumlah record, unique source_ref_id, serta checksum referensi katalog.
Checksum katalog hanya mencakup ID pada file, sehingga tidak sama dengan checksum
seluruh source jika sebelumnya sudah ada event BMKG lain.

### Cocokkan dashboard dengan Audit API

Gunakan dataset stabil selama pengambilan bukti. Verifier membaca seluruh halaman
Audit API, memeriksa ordering/ID duplikat, menghitung checksum secara independen,
kemudian membandingkan total, jenis bahaya, severity, wilayah, dan setiap bucket
waktu dengan hasil analitik:

```sh
python3 scripts/verify-analytics.py \
  --env-file ../../infrastructure/.env.runtime \
  --from 2026-11-01T00:00:00Z --to 2026-11-19T00:00:00Z \
  --interval day --source BMKG
```

Gunakan `--interval month` atau `year` untuk katalog yang mencakup bertahun-tahun.
Tambahkan `--expected-count` dan `--expected-checksum` dari referensi asisten
untuk memeriksa keseluruhan source, serta `--output /path/bukti.json` untuk menyimpan
hasil. Tanpa referensi tersebut, hasil hanya membuktikan kecocokan internal dengan
Audit API. Respons parsial, duplikat, data berubah saat export, atau angka yang
berbeda menghasilkan exit nonzero; hasil tersebut tidak dinyatakan lulus.

Untuk demo P1, simpan hasil sebelum gangguan, rekam response partial selama worker
hilang, lalu ulangi verifier setelah recovery dan kehilangan worker berikutnya.
Gunakan Tester Agent resmi pada VM untuk pembuktian akhir. Tool di bagian ini tidak
mematikan node atau menghapus disk. Pengukuran beban saat import/lonjakan wilayah,
dataset asisten yang sebenarnya, dan laporan final tetap memerlukan eksekusi demo.
