# AAT — data dan penyimpanan

Go + Gin, PostgreSQL, dan pgx. Tersedia: mock BMKG/PVMBG, pemetaan HazardEvent,
penyimpanan, dan API internal Aggregator. **Polling otomatis, Client-Facing API,
Auth Service, broker/consumer, dan load test sistem belum tersedia.**
Broker yang direncanakan untuk publikasi event adalah **NATS**.

## Alur data

Alur yang diwajibkan spesifikasi (panah menunjukkan pemanggil → tujuan):

```text
Aggregator → polling BMKG / PVMBG → normalisasi → PostgreSQL
Client → Client-Facing API → API Aggregator → PostgreSQL
```

Mock bersifat pasif; tidak membaca atau mengirim data ke Aggregator.
Saat ini ingest dilakukan manual lewat HTTP. Hanya Aggregator yang mengakses DB;
PostgreSQL berada di network internal tanpa port host.

## Menjalankan

Memerlukan Docker Compose v2. Jalankan dari root repository:

```sh
cd code/apps/aat
# Hanya jika .env belum ada; isi kredensial acak yang berbeda untuk tiap service.
cp -n .env.example .env
# Edit .env sebelum menjalankan stack.
docker compose -p aat-part1 up -d --build
docker compose -p aat-part1 ps
```

Konfigurasi ada di [.env.example](.env.example). Port host default:
BMKG `8081`, PVMBG `8082`, Aggregator `8083`; semuanya terikat localhost.
Setiap service menyediakan `GET /health` tanpa autentikasi.

| Konfigurasi | Default / ketentuan |
|---|---|
| `BMKG_API_KEY`, `PVMBG_TOKEN`, `AGGREGATOR_TOKEN` | Wajib, berbeda; token Aggregator hanya untuk internal |
| `POSTGRES_USER`, `POSTGRES_DB`, `POSTGRES_PASSWORD` | Wajib; gunakan password hex agar aman untuk format connection string |
| `BMKG_DELAY_MS` | `100`, rentang 50–150 ms |
| `PVMBG_DELAY_MIN_MS`, `PVMBG_DELAY_MAX_MS` | `500`, `3000`; rentang 0–10000 ms |
| `EVENT_INTERVAL_SECONDS` | `10`, rentang 1–10 detik |
| `DB_MAX_CONNS` | `5` per Aggregator |

Mock dimulai dengan 20 record, lalu menghasilkan satu record per interval.
Filter `since` inklusif; warning mengikuti waktu gempa. State mock reset saat restart.

## Endpoint

| Service / autentikasi | Endpoint |
|---|---|
| BMKG — `X-BMKG-Key` | `GET /seismic-events`, `GET /tsunami-warnings`; keduanya menerima `?since=` |
| PVMBG — Bearer `PVMBG_TOKEN` | `GET /volcanic-reports?since=`, `POST /admin/schema-version`, `POST /admin/outage` |
| Aggregator — Bearer `AGGREGATOR_TOKEN` | `POST /internal/ingest/bmkg`, `POST /internal/ingest/pvmbg`, `GET /internal/hazards` |

Body admin: `{"enabled":true|false}`. Body ingest BMKG:
`{"seismic_events":[...],"tsunami_warnings":[...]}`; PVMBG: `{"volcanic_reports":[...]}`.
Respons ingest: `{"changed":N,"items":[...]}`; payload identik menghasilkan `changed:0`.

Pembacaan hazards menerima `source`, `limit` (1–1000), dan `after`; respons berisi
`items` dan `next_after`. Pagination berdasarkan ID, bukan cursor perubahan.
Body maksimal 2 MiB; input tidak valid → 400, auth salah → 401, DB gagal → 503.
Batch gagal di-rollback. Log JSON menyertakan `X-Correlation-ID`.

Contoh ingest, perubahan skema, outage, dan auth silang: **[panduan demo](docs/demo.md)**.

## Struktur dan penyimpanan

- `bmkg/`, `pvmbg/`: masing-masing memiliki `cmd/server`, `internal/mock`, dan Dockerfile.
  Di dalam mock, `model/` menangani data/state, `controller/` menangani HTTP,
  dan `routes.go` mendaftarkan endpoint.
- `aggregator/`: route/handler di `cmd/server`, pemetaan dan storage di `internal/aggregate`.
- `internal/httpkit/`: helper transport/config/log bersama; business logic tetap per service.

Field standar HazardEvent memakai kolom bertipe; atribut dinamis memakai `attributes`
JSONB. Payload sumber disimpan sebagai JSONB untuk korelasi/pemetaan ulang.
Field tambahan diteruskan ke attributes tanpa migrasi; perubahan tipe atau penghapusan
field wajib ditolak. Warning mengoverride severity gempa; ID tetap stabil.

[Schema SQL](aggregator/internal/aggregate/schema.sql) memigrasikan tabel dokumen lama
secara transaksional saat startup. **Update seluruh replica Aggregator bersama** karena
binary lama membutuhkan kolom `document`. Data lama dipertahankan.

PostgreSQL dipilih untuk constraint dan transaksi plus JSONB; SQLite membatasi replikasi
berbasis file, sedangkan MongoDB menambah model operasional berbeda.

## Tes dan operasional

Memerlukan Go 1.25+. Dari direktori AAT:

```sh
go test ./...
go vet ./...
# Opsional: PostgreSQL khusus tes, setiap kasus memakai schema terpisah.
AAT_TEST_DATABASE_URL='postgres://user:password@localhost/testdb?sslmode=disable' \
  go test ./aggregator/internal/aggregate -run TestStoreSchemaAndIngestion -v

# Rebuild satu service tanpa restart service lain.
docker compose -p aat-part1 up -d --build --no-deps pvmbg
# Hentikan stack; data PostgreSQL tetap tersimpan.
docker compose -p aat-part1 down
```

Untuk menjalankan di host: `go run ./<service>/cmd/server` setelah mengekspor env.
Aggregator membutuhkan `DATABASE_URL` yang terjangkau; jangan bentrok dengan port Compose.
Compose biasa cukup untuk satu Aggregator. `compose.scale.yaml` opsional untuk replica
lokal, menghapus akses host Aggregator, dan belum menyediakan reverse proxy.

Demo ini belum membuktikan polling, stale status, scope/refresh token client, atau
publikasi event andal (outbox/delivery policy masih diperlukan). HTTP untuk demo lokal.
Implementasi dibantu Codex; anggota perlu memahami, memverifikasi, dan mendeklarasikan
penggunaannya dalam laporan. Pembagian kerja ada di `tasks.local.md` (lokal).
