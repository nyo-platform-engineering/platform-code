# M2 P3 — inbox, perubahan skema, dan replay

Status: jalur polling BMKG/PVMBG sudah memakai durable inbox. Field reports dan
penguatan consumer/notifikasi belum termasuk iterasi ini.

Implementasi dibantu AI. Pengujian lokal menggunakan PostgreSQL 17; tes Citus
tersedia tetapi belum dijalankan pada formation asli.

## Alur pesan

1. Pemilik polling mengambil respons mentah dari seluruh endpoint sumber.
2. `CaptureRawPoll` menyimpan bytes asli pada inbox dan commit sebelum decode JSON.
3. Worker memecah respons menjadi record, lalu menyimpan record secara durable.
4. Setiap record diproses dalam transaksi tersendiri. Hazard, outbox, dan status
   `processed` harus commit bersama. Gagal commit berarti record tetap menunggu.
5. Payload yang sudah berhasil diproses dikosongkan. Metadata dedup tetap disimpan.
   Payload yang gagal tetap utuh sebagai `bytea`, termasuk bytes yang bukan UTF-8.

`fetch_cursor` sekarang menunjukkan data yang sudah diterima secara durable,
bukan data yang seluruhnya sudah menjadi HazardEvent. Cursor hanya maju ketika
envelope berhasil dipecah dan record-nya sudah disimpan; status pemrosesan
masing-masing record dicatat terpisah. Jika proses mati setelah capture, pemilik
berikutnya melanjutkan inbox sebelum mengambil data baru, termasuk saat upstream
sedang outage.

Untuk record yang punya identitas, versi berikutnya dengan `source_ref_id` sama
menunggu seluruh pendahulunya selesai. Key lain tetap diproses. Warning BMKG
memakai `related_event_id` sebagai ordering key agar sejalan dengan gempa terkait.
Payload yang tidak dapat diidentifikasi disimpan untuk inspeksi; sistem tidak
mengklaim urutan per key yang identitasnya tidak diketahui.

## Status dan batas payload

| Status | Perilaku |
| --- | --- |
| `retrying` | Menunggu proses atau mencoba ulang kegagalan sementara; backoff 2–64 detik ditambah jitter <500 ms |
| `parked` | Memerlukan perubahan parser/kebijakan atau perbaikan sumber; tidak diulang otomatis |
| `processed` | Efek sudah commit; hilang dari daftar unprocessed dan payload dikosongkan |

Parser menerima satu array JSON, UTF-8 valid, ukuran maksimal 2 MiB, kedalaman
maksimal 64. Pesan invalid disimpan utuh beserta sumber, waktu, alasan, dan attempts.
Kegagalan satu record tidak membatalkan record lain; envelope yang tidak dapat
diurai diparkir utuh.

Capture memiliki batas transport 8 MiB per respons. Respons antara 2 dan 8 MiB
tetap disimpan utuh lalu diparkir sebagai oversize. Respons lebih besar dari 8 MiB
ditolak sebelum dianggap diterima; cursor tidak dimajukan untuk respons itu dan
tidak ada klaim bahwa payload terpotong sudah disimpan utuh.

Dedup memakai SHA-256 atas sumber, endpoint, jenis pesan, dan bytes payload.
Respons/record identik tidak membuat pekerjaan baru. Karena mock belum memberi
revision ID, perubahan A→B→A dengan bytes A persis sama tidak dapat dibedakan
dari pengiriman ulang A. Sumber dengan revisi seperti itu membutuhkan version ID
sebelum jaminan ordering diperluas.

## Skema PVMBG

`POST /admin/schema-version` tetap menerima `{"enabled":true|false}` untuk M1.
Mode baru memakai `{"mode":"..."}`; jangan mengirim mode dan enabled sekaligus.

| Mode mock | Perubahan |
| --- | --- |
| `original` | Skema awal |
| `additive` | Menambahkan `confidence_level` |
| `rename_field` | `alert_level` menjadi `level` |
| `type_change` | `eruption_count_24h` menjadi string integer |
| `missing_required` | Menghilangkan `eruption_count_24h` |

`PVMBG_SCHEMA_POLICY=strict` adalah default: tambahan field diterima, tiga
perubahan breaking diparkir. Setelah dukungan disepakati, deploy Aggregator dengan
`PVMBG_SCHEMA_POLICY=v2` dan minta replay pesan terkait. Kebijakan v2 menerima alias
`level`, string integer kanonik, serta count yang tidak tersedia sebagai atribut
yang absen. Count tidak diganti dengan nol; severity tetap berasal dari alert
level. Dua field level sekaligus, level tidak dikenal, count negatif, dan tipe
lain yang tidak sesuai tetap ditolak.

Konfigurasi tersedia di `.env.example`, Compose, dan values Aggregator Kubernetes.
Mengubah `.env` saja tidak memperbarui container yang sudah berjalan.

## Audit dan Internal Ops

- `GET /internal/audit/unprocessed`: token Audit asisten; source/limit/cursor,
  default limit 100, maksimum 1000, next_cursor null pada akhir. Cursor terikat
  filter sumber. Respons berisi metadata sesuai Lampiran A, tanpa payload raw.
- `GET /ops/unprocessed`: access token client dengan identitas `analyst`.
- `GET /ops/unprocessed/<message_id>`: detail, termasuk `raw_base64`.
- `POST /ops/unprocessed/<message_id>/replay`: mengantrekan ulang pesan; tidak
  menjalankan pemrosesan di handler HTTP. Pesan yang sudah selesai mendapat 409.

Media (`public`) dan Tim Lapangan (`responder`) ditolak 403 sebelum mengakses
storage. Token tidak aktif ditolak 401. Client API memanggil endpoint Ops internal
Aggregator memakai credential service; user token tidak diteruskan ke storage.
Audit token tidak memberi akses ke raw payload atau replay.

Contoh setelah login sebagai Internal Ops (variabel token tidak dicetak):

```sh
curl -H "Authorization: Bearer $OPS_ACCESS_TOKEN" \
  "$CLIENT_URL/ops/unprocessed?source=PVMBG&limit=100"
curl -X POST -H "Authorization: Bearer $OPS_ACCESS_TOKEN" \
  "$CLIENT_URL/ops/unprocessed/$MESSAGE_ID/replay"
```

## Integrasi Citus P1

`hazard_events` tetap didistribusikan oleh Citus berdasarkan `source_ref_id`.
`poll_owners`, `poll_states`, `inbox_messages`, dan `hazard_outboxes` tetap lokal
pada coordinator. Migrasi composite primary key milik P1 tetap dipertahankan.
Semua koneksi polling harus menuju coordinator (group 0); koneksi ke worker
ditolak. Sesi pemilik menetapkan `citus.multi_shard_commit_protocol=2pc`.

Worker memakai adapter PostgreSQL pada transaksi coordinator yang sama; adapter
ini tidak mengganti Citus atau menulis langsung ke shard. Citus merutekan SQL
canonical ke worker. Status inbox baru menjadi processed setelah commit berhasil.
Jika savepoint atau koneksi distributed gagal dipulihkan, transaksi dibatalkan
dan pesan tetap menunggu. Coordinator tunggal tetap bergantung pada topologi P1;
iterasi ini tidak menambahkan HA coordinator.

Rujukan: [Citus DML](https://docs.citusdata.com/en/stable/develop/reference_dml.html)
dan [konfigurasi Citus](https://docs.citusdata.com/en/stable/develop/api_guc.html).

## Pengujian

```sh
export AAT_TEST_DATABASE_URL='postgres://USER@HOST:PORT/TEST_DB?sslmode=disable'
go test ./... -count=1
go test -race ./... -count=1
```

Gunakan database uji terpisah. Suite memakai schema sementara dan menguji capture
sebelum crash, takeover, isolasi payload rusak, replay berurutan, dedup, transient
retry, rollback saat penanda processed gagal, kontrol akses, serta trace/outbox.

Untuk formation Citus uji dengan tiga worker aktif:

```sh
AAT_TEST_CITUS=1 go test ./aggregator/cmd/server ./aggregator/internal/storage \
  -run 'TestInboxCitus|TestCitusDistribution' -count=1
```

Tes tersebut memeriksa distribusi, protokol 2pc, rollback canonical/outbox/status,
serta bahwa tabel koordinasi/inbox tidak di-shard. PostgreSQL biasa tidak dapat
menggantikan bukti ini. Pengujian kehilangan node, partisi jaringan, dan failover
Patroni tetap dilakukan pada cluster bersama pemilik P1.
