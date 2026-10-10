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
