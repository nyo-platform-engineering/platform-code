# M2 P1 — Distribusi data, pemulihan, dan analitik

Jalankan dari `code/infrastructure` dengan cluster aktif dan `curl` tersedia:

```sh
task m2:p1
```

Atur katalog, checksum referensi, dan worker di `CONFIG` pada
[test-problem-1.py](test-problem-1.py). Tekan **Enter** untuk melanjutkan tiap tahap.
Terminal menampilkan ringkasan; output lengkap (tujuan, langkah, hasil, batasan)
tersimpan di `console.log` pada folder evidence. Progress terminal dibatasi tiap
30 detik. Set `CONFIG.verbose_output=True` untuk detail penuh di terminal.
Mode otomatis mematikan worker dan menghapus data store lokal sebelum membangunnya kembali.

## Freeze otomatis untuk checkpoint

Jalankan langsung `task m2:p1`; **tidak perlu `task polling:freeze` lebih dulu**.
P1 menjeda fetch BMKG/PVMBG otomatis pada tahap **global-reads** dan
**dashboard-analytics**, lalu mengaktifkannya kembali sebelum melanjutkan.
Tidak ada prompt freeze tambahan. Utility yang sama menunggu pod Aggregator lama
berhenti, menguras inbox mock, serta mengembalikan URL dan pengaturan Argo.
Tunnel pengujian disambungkan ulang setelah rollout.

Import, lonjakan regional, worker loss, rejoin, serta verifikasi recovery tetap
memakai polling dan probe aktif. Runner menolak freeze di dalam monitor tersebut.
Satelit, Pantai, dan request ingest langsung tetap aktif. Checkpoint yang dijeda
mewajibkan count/checksum identitas BMKG/PVMBG tetap sama; direct write bersamaan
akan menggagalkan checkpoint, bukan diklaim sebagai snapshot yang tenang.

Freeze dipulihkan jika checkpoint gagal atau diinterupsi. Jika proses mati atau
pemulihan gagal, jalankan `task polling:resume` dari infrastructure sebelum tes
berikutnya. Riwayat jeda tersimpan di `polling-freezes.json` dan `result.json`.
Error polling/source-status saat jeda adalah efek maintenance yang disengaja.

## Kriteria penyelesaian

| No. | Yang dibuktikan | Test case |
| --- | --- | --- |
| 1 | Data terbagi ke beberapa shard; tidak ada shard memuat seluruh data. Primary tersebar pada lebih dari satu node; laporkan shard primary/replica per node. Node boleh menyimpan replica shard milik node lain. | Group 1: `catalog_distribution` |
| 2 | Ukur beban tiap node saat impor dan lonjakan gempa satu wilayah; jelaskan ketidakmerataannya. | Group 1: `catalog_distribution`, `uneven_load` |
| 3 | Baca 50 event terbaru lintas penyimpanan, ikuti pagination, dan pastikan urutannya benar. | Group 1: `global_reads` |
| 4 | Sebelum worker dimatikan, setiap shard wajib punya replica siap pada node berbeda. Setelah worker mati dan datanya dihapus, event/checksum tetap terjaga. Selama pemulihan, respons lengkap atau parsial dengan penanda eksplisit; error dan permintaan melewati batas waktu menggagalkan tes. | Group 2: `lose_worker` |
| 5 | Worker berdisk kosong bergabung dan kembali menyimpan data sambil pembacaan serta penulisan terus dipantau. | Group 2: `rejoin_worker` |
| 6 | Pulihkan replika, matikan worker lain, lalu verifikasi data bertahan dan pulihkan redundansi lagi. | Group 2: `restored_resilience` |
| 7 | Dashboard analitik menampilkan gempa, tsunami, gunung berapi, kebakaran hutan, dan pesisir: timeline dengan rentang waktu, jumlah per severity, dan jumlah per `area_name`. | Group 3: `dashboard` |

File grup: [Group 1](group1_distribution.py), [Group 2](group2_recovery.py),
[Group 3](group3_analytics.py).

Angka dashboard dibandingkan dengan penghitungan dari Audit API lintas penyimpanan.
Saat node mati, monitor memeriksa respons analytics dan metadata parsial.
Chart, pilihan waktu, dashboard tetap tampil, dan penanda angka tidak lengkap
harus ditunjukkan langsung di peramban, terpisah dari dashboard metrik.

## Bukti dan batasan

Hasil tersimpan di `code/apps/aat/tests/m2/p1/evidence/m2-p1-*/`: `result.json`,
output lengkap `console.log`, rekaman HTTP, metrik per node, penempatan data,
dan checkpoint checksum/analitik. `catalog-shards.json` mencatat seluruh shard,
jumlah baris pada primary, serta ID shard primary/replica per node. Pemeriksaan
ini membaca metadata Citus dan tabel shard melalui `kubectl exec`/`psql`, lalu
memastikan kesiapan primary dan replica melalui Patroni (`/primary`, `/replica?lag=0`).
HTTP 503 dari pemeriksaan Patroni menampilkan status WAIT dan mengulang pembacaan
role/metadata hingga `CONFIG.shard_ready_timeout` (default 180 detik). Jika belum
pulih, tahap gagal dengan alasan jelas; HTTP error dari endpoint data tetap gagal.
`*-pre-loss-shards.json` disimpan sebelum tiap penghapusan worker; `*-rejoin-shards.json`
mencatat redundansi setelah rebuild. Tidak diperlukan perubahan atau deploy API.
Sebelum setiap run, pilih apakah folder evidence P1 lama dihapus; **Enter** menyimpannya.

Dataset bawaan adalah **mock pengembangan**, bukan katalog/checksum resmi asisten.
Untuk bukti penyelesaian, gunakan dataset resmi, sertakan pengukuran disk/write IO
serta penjelasan beban dari Grafana, dan rekam demo dashboard. Metrik otomatis
mencakup CPU/memori; lolos tes otomatis belum berarti seluruh bukti manual lengkap.

Jika pod Aggregator berganti, tunnel `kubectl` yang berhenti disambungkan ulang.
Control plane diprioritaskan; fallback boleh memakai Aggregator Ready di node lain,
kecuali worker yang akan dimatikan. Kandidat dan hasil koneksi disimpan di
`endpoint-connections.json`.
Hanya GET yang boleh diulang sekali setelah tunnel mati; error HTTP, timeout, dan tulis
dengan hasil tidak diketahui tetap gagal/dicatat. Reconnect ikut memakai batas waktu request
dan tercatat di `http.jsonl` serta `result.json`.

Runner mengantrekan `/internal/hazards` dan `/internal/analytics` agar probe sendiri
tidak berebut satu slot baca pada konfigurasi lab. Kedua endpoint juga berbagi jeda
minimal `CONFIG.read_interval` (default 0,1 detik, maksimal 10 request/detik) agar
pagination cepat tidak menghabiskan burst batas 20 RPS per pod. Waktu antre dan
jeda tercakup dalam deadline request serta dicatat di `http.jsonl`. Penulisan dan
Audit tidak diberi jeda ini; penulisan tetap berjalan bersamaan.
429 dari server tetap menggagalkan tahap dan penyebabnya dicetak langsung di terminal.
