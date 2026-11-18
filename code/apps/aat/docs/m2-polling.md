# M2 P2 — ownership polling dan kontrak integrasi P1

Status: fondasi kode dan integration test lokal tersedia. Ini **belum** hasil
uji clock skew/freeze VM/kehilangan node pada cluster, load 10.000 VU, throughput
2×, atau rolling update tanpa kegagalan. Jalur inbox/replay P3 dijelaskan di
[m2-ingestion.md](m2-ingestion.md); field reports belum termasuk.

Implementasi dibantu AI; perilaku berikut diverifikasi dengan integration test
PostgreSQL nyata. Kode dan kontraknya perlu dipahami oleh pemilik bagian.

## Mekanisme saat ini

- Setiap sumber BMKG/PVMBG memiliki satu session advisory lock PostgreSQL.
  Lock memakai database/schema/sumber yang sama di semua replika.
- Pemilik mempertahankan satu koneksi fisik selama masih aktif. Follower tidak
  mengirim request upstream ketika acquire lock gagal. Replika tetap melayani API.
- Setiap acquire menaikkan `poll_owners.epoch`; identitas pemilik dan backend PID
  disimpan bersama. Ini bukan lease berdasarkan jam Aggregator atau timestamp DB.
- Semua operasi pemilik terikat ke `sql.Conn` yang sama; koneksi yang mati tidak
  boleh transparan diganti koneksi baru sambil membawa hak tulis lama.
- Validasi dilakukan sebelum setiap request upstream. Transaksi ingest mengunci
  row ownership dan memeriksa source, epoch, owner, serta backend PID sebelum
  menulis. Koneksi tertutup atau generation yang tidak sesuai ditolak.
- PostgreSQL memutus session idle setelah 20 detik. Ini membatasi lamanya lock
  tertahan bila proses/VM pemilik freeze di luar transaksi, tanpa mengandalkan
  timer pada VM yang membeku. Statement dan idle transaction dibatasi 5 detik;
  PostgreSQL 17+ juga mendapat batas total transaksi 10 detik.
- Follower mencoba lagi tiap interval polling (default 3 detik), dengan timeout
  acquire 5 detik. Anggaran nominal takeover sesi idle sekitar 20 + 3 + 5 detik;
  batas ≤60 detik harus tetap diukur di cluster, termasuk konektivitas/storage.
- Close/shutdown menutup koneksi fisik dan melepas lock. SIGTERM membatalkan
  poller/publisher, menunggu worker, serta memberi HTTP waktu drain 10 detik.

## Cursor dan transaksi

`poll_states.fetch_cursor` terpisah dari `last_success_at`.

- Cursor BMKG berasal dari maksimum `occurred_at` hasil seismic. Endpoint warning
  dipanggil setelah seismic. `estimated_arrival` warning bukan cursor.
- Cursor PVMBG berasal dari maksimum `reported_at` hasil sumber.
- Request berikutnya memakai `since` inklusif. Rekaman pada batas boleh diterima
  ulang; upsert idempoten mencegah event/outbox baru untuk data identik.
- Respons kosong mempertahankan cursor; cursor tidak pernah memakai waktu lokal
  Aggregator dan tidak mundur. Instalasi dari M1 akan melakukan import awal lagi
  bila `fetch_cursor` masih kosong, alih-alih memakai cursor caller lama.
- API sinkron `IngestPollBatch` menyimpan hazard, outbox, status sukses, dan cursor
  pada satu transaksi. Polling P3 memakai durable inbox: capture commit lebih dulu,
  lalu setiap record dan status processed-nya commit bersama efek canonical/outbox.
  Cursor menandai penerimaan durable; lihat panduan ingestion untuk detailnya.
- `last_success_at` memakai waktu database untuk metadata freshness, bukan untuk
  menentukan kepemilikan atau progress data. Timestamp error lokal hanya diagnosis.

Kontrak cursor ini cocok dengan mock saat ini yang menghasilkan urutan deterministik
berdasarkan waktu. Late historical updates atau sumber yang memundurkan waktu dan
menghasilkan revisi data lama memerlukan offset/version yang lebih kuat atau
strategi overlap yang disepakati; jangan memperluas klaimnya ke sumber lain.

## Kontrak yang harus dipertahankan P1

API Go baseline (lihat `aggregator/internal/controller/poll_session.go`):

```text
AcquirePollSession(ctx, source) -> pinned session + {source, epoch, owner, backend_pid}
ValidatePollOwner(ctx, source) -> valid / fenced / unavailable
IngestPollBatch(ctx, source, batch, sourceCursor)
  -> atomic hazard + outbox + progress commit / no commit
PollSession.Close() -> release ownership; old controller permanently unusable
```

Jika P1 memindahkan database ke service storage terpisah:

1. Letakkan session/koordinasi pada pemilik storage atau sediakan API koordinasi
   ekuivalen. Service lain tidak diberi akses database milik service tersebut.
2. Token harus meningkat pada takeover dan tidak boleh dipakai ulang setelah
   failover, restore, atau kehilangan satu node. Failover database asynchronous
   yang mengembalikan epoch lama tidak memenuhi kontrak ini.
3. Pemeriksaan token dan commit data/progress harus atomik terhadap takeover.
   Check token lewat satu API lalu tulis tanpa fencing di API lain tidak cukup.
4. Tentukan cara transaksi outbox/progress bekerja jika data berada di beberapa
   shard: atomic commit atau protokol recovery idempoten dengan bukti ekuivalen.
5. Saat otoritas/storage tidak dapat diverifikasi, fail closed untuk polling dan
   tulisannya. Baca parsial tetap menjadi tanggung jawab endpoint P1.

Baseline ini membutuhkan PostgreSQL 14+ dan akses koneksi langsung/session pooling.
Transaction pooling tidak mempertahankan session advisory lock. Gunakan
`DB_MAX_CONNS >= 3`; sampai dua koneksi per pemilik proses dipin untuk polling,
sehingga default 5 menyisakan koneksi API/publisher. Capacity connection storage
perlu dihitung kembali untuk pengujian 1/3/5 replika.

## Batas jaminan upstream

Satu pemilik aktif menghindari polling berlipat dari replika sehat. Request membawa
`X-Correlation-ID`, `X-Poll-Owner`, dan `X-Poll-Epoch`; log outbound menyertakan
identitas ownership agar bisa dicocokkan dengan log upstream.

Fencing storage belum berarti exactly-once HTTP. Proses dapat freeze tepat setelah
validasi, sebelum mengirim request; hasil request juga bisa tidak diketahui setelah
koneksi putus. Respons tertunda dari pemilik lama tidak boleh tersimpan setelah
takeover, tetapi request recovery mungkin mengulang pembacaan upstream. Uji batas
ini dan cocokan definisi pengujian P2.2 dengan asisten; bila harus menolak request
lama di sisi upstream, diperlukan gate/fencing di penerima atau protokol request
idempoten. Jangan menyatakan P2.2 final selesai hanya dari row storage yang unik.

## Mengulang verifikasi

Dari `code/apps/aat`, gunakan database uji terpisah, bukan database demo/produksi:

```sh
export AAT_TEST_DATABASE_URL='postgres://USER@HOST:PORT/TEST_DB?sslmode=disable'
go test ./... -count=1
go test -race ./aggregator/cmd/server ./aggregator/internal/controller ./internal/httpkit -count=1
```

Tes membuat schema terisolasi dan membersihkannya. Akun penguji harus dapat
membuat schema dan menghentikan backend miliknya (`pg_terminate_backend`).
Tanpa variabel database, integration test dilewati; unit test saja bukan buktinya.

Suite `TestM2*` mencakup exclusion per sumber, source independen, epoch takeover,
session termination/idle expiry tanpa cleanup client, stale write/status rejection,
rollback hazard/outbox/cursor, tiga poller dengan satu pemilik, shutdown release,
serta cursor inklusif dari waktu sumber. Idle expiry dipercepat menjadi 200 ms
khusus satu test; itu simulasi sesi idle, bukan pembuktian suspend VM sebenarnya.

Pekerjaan berikutnya: field reports, penguatan consumer/notifikasi, serta
pengujian cluster dan performa P2/P3. Inbox/replay dan Audit unprocessed sudah
ditambahkan; tes formation Citus masih perlu dijalankan pada cluster asli.

Verifikasi lokal 18 November 2026: `go test ./... -count=1` lulus dengan
`AAT_TEST_DATABASE_URL` terisi pada PostgreSQL 17 sementara. Pemeriksaan
`go test -race ./aggregator/cmd/server ./aggregator/internal/controller
./internal/httpkit -count=1` juga lulus. Tujuh test `TestM2*` mencakup skenario di
atas, termasuk respons upstream tertunda setelah takeover. Pengujian M1 tetap
lulus; satu assertion lama diperbaiki agar mengabaikan whitespace JSONB.
