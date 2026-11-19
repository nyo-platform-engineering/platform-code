# M2 P1 — Distribusi data, pemulihan, dan analitik

Jalankan dari `code/infrastructure` dengan cluster aktif dan `curl` tersedia:

```sh
task m2:p1
```

Atur katalog, checksum referensi, dan worker di `CONFIG` pada
[test-problem-1.py](test-problem-1.py). Tekan **Enter** untuk melanjutkan tiap tahap.
Output tiap tahap menjelaskan tujuan kriteria, langkah pemeriksaan, hasil, batasan, dan lokasi bukti.
Mode otomatis mematikan worker dan menghapus data store lokal sebelum membangunnya kembali.

## Kriteria penyelesaian

| No. | Yang dibuktikan | Test case |
| --- | --- | --- |
| 1 | Katalog gempa tersebar; tidak ada node menyimpan seluruh dataset. Laporkan jumlah data dan primary per node. | Group 1: `catalog_distribution` |
| 2 | Ukur beban tiap node saat impor dan lonjakan gempa satu wilayah; jelaskan ketidakmerataannya. | Group 1: `catalog_distribution`, `uneven_load` |
| 3 | Baca 50 event terbaru lintas penyimpanan, ikuti pagination, dan pastikan urutannya benar. | Group 1: `global_reads` |
| 4 | Setelah worker mati dan datanya dihapus, event/checksum tetap terjaga. Selama pemulihan, respons lengkap atau parsial dengan penanda eksplisit; error dan permintaan melewati batas waktu menggagalkan tes. | Group 2: `lose_worker` |
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
rekaman HTTP, metrik per node, penempatan data, dan checkpoint checksum/analitik.
Sebelum setiap run, pilih apakah folder evidence P1 lama dihapus; **Enter** menyimpannya.

Dataset bawaan adalah **mock pengembangan**, bukan katalog/checksum resmi asisten.
Untuk bukti penyelesaian, gunakan dataset resmi, sertakan pengukuran disk/write IO
serta penjelasan beban dari Grafana, dan rekam demo dashboard. Metrik otomatis
mencakup CPU/memori; lolos tes otomatis belum berarti seluruh bukti manual lengkap.

Jika pod Aggregator berganti, tunnel `kubectl` yang berhenti disambungkan ulang.
Hanya GET yang boleh diulang sekali setelah tunnel mati; error HTTP, timeout, dan tulis
dengan hasil tidak diketahui tetap gagal/dicatat. Reconnect ikut memakai batas waktu request
dan tercatat di `http.jsonl` serta `result.json`.
