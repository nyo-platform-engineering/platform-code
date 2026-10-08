# Hasil load testing M1 P2.2

**Hasil: LULUS.** Run `load-4261ca1345634033babc880f656f7c15`.

Dijalankan 08 October 2026, 16:43:14 sampai 16:44:20 WIB, termasuk setup/monitor. Fase sustained k6 65 detik, 50 VU tanpa think time, HTTP keep-alive, page limit 100. Identitas public/responder/analyst dan source BMKG/PVMBG dicampur. TTL 60 detik, refresh otomatis tanpa login ulang selama beban.

| Ukuran | Hasil |
| --- | --- |
| Request hazards total | 641,715 |
| Respons 200 valid | 42,126 |
| Penolakan terkontrol 429 | 599,589 (93.44%) |
| Unexpected failures | 0 |
| Error rate, denominator tanpa 429 | 0.00% (<1%) |
| Throughput seluruh respons | 9872.54 req/s |
| Throughput respons sukses | 648.09 req/s |
| Throughput penolakan 429 | 9224.45 req/s |
| Refresh sukses / refresh 429 | 50 / 0 |

| Latensi HTTP | p50 (ms) | p95 (ms) | p99 (ms) |
| --- | ---: | ---: | ---: |
| Semua respons, termasuk 429 | 2.510 | 21.611 | 38.437 |
| Hanya respons 200 valid | 27.505 | 46.385 | 57.761 |

Latensi adalah send+wait+receive dari k6. Setup/login dan refresh tidak masuk metrik hazards. Throughput dibagi fase sustained 65 detik; request terakhir diberi graceful completion maksimal 10 detik. Persentil gabungan banyak dipengaruhi penolakan cepat; gunakan baris sukses untuk latensi pembacaan data.

## Bukti sustained dan koneksi

| Jendela relatif awal VU (detik) | Request | Sukses | 429 | Kegagalan |
| --- | ---: | ---: | ---: | ---: |
| 0-5 | 54,595 | 3,602 | 50,993 | 0 |
| 5-10 | 55,224 | 3,525 | 51,699 | 0 |
| 10-15 | 55,148 | 3,522 | 51,626 | 0 |
| 15-20 | 54,110 | 3,479 | 50,631 | 0 |
| 20-25 | 49,846 | 3,236 | 46,610 | 0 |
| 25-30 | 45,969 | 3,189 | 42,780 | 0 |
| 30-35 | 48,767 | 3,187 | 45,580 | 0 |
| 35-40 | 47,342 | 3,168 | 44,174 | 0 |
| 40-45 | 45,995 | 3,114 | 42,881 | 0 |
| 45-50 | 40,811 | 2,765 | 38,046 | 0 |
| 50-55 | 44,082 | 2,959 | 41,123 | 0 |
| 55-60 | 49,475 | 3,132 | 46,343 | 0 |
| 60-65 | 50,351 | 3,248 | 47,103 | 0 |

Koneksi TCP inbound Client API yang teramati per sampel: `[51, 51, 51, 51, 51, 51, 50, 50, 50, 50, 50, 50, 50]`. Setiap sampel menunjukkan sedikitnya 50 koneksi aktif. Penghitungan TCP dapat mencakup koneksi preflight/health tambahan; beban k6 sendiri ditetapkan pada tepat 50 VU. Semua sembilan container tetap Running dengan ID/restart count yang sama dan tanpa OOM. Health Client tetap sukses; cursor polling kedua sumber maju.

## Konfigurasi dan interpretasi

Tool: `k6 v2.0.0 (commit/devel, go1.26.3, darwin/arm64)`. Host `Darwin/arm64`, Docker `29.5.3`, 12 CPU tersedia dan 7.75 GiB memori Docker. Generator dan server memakai mesin yang sama; angka dapat berubah pada mesin/payload lain.

Client concurrency 16, Auth concurrency 32, pool DB 5. Poll interval 3 detik dan timeout 4 detik; timeout Client 3 detik. PVMBG delay tetap 3000 ms, BMKG 100 ms. Tidak ada tuning aplikasi atau pelonggaran TTL untuk membuat load test lulus.

Sebanyak 93.44% request ditolak 429. Beban sengaja mengirim ulang tanpa jeda atau backoff, sehingga ini hasil saturasi. P2.2 memperbolehkan penolakan terkontrol jika jumlahnya dilaporkan. Sistem memenuhi kriteria ketahanan/error rate, tetapi hasil ini tidak menyatakan penerimaan tinggi: kapasitas sukses terukur hanya 648.09 req/s. Client nyata sebaiknya menghormati Retry-After; peningkatan acceptance membutuhkan evaluasi concurrency/pool DB/scaling terpisah.

## File bukti dan cara mengulang

- [result.json](result.json): konfigurasi, throughput, p50/p95/p99, error rate, status counts, time windows, monitoring container/TCP, dan polling sebelum/sesudah.
- [k6-summary.json](k6-summary.json): metrik dan hasil threshold k6; setup data/token dikeluarkan.
- [k6-console.txt](k6-console.txt): log proses k6 (kosong bila tidak ada warning/error).
- [Metode dan perintah](../../load-testing.md): cara menjalankan kembali pengujian.

Run awal disimpan di `../load-normal/`; hasil di dokumen ini memakai run final dengan rekaman TCP otomatis. Load testing selesai, sedangkan laporan PDF M1, Form Penggunaan AI, commit/tag dan administrasi pengumpulan belum diselesaikan oleh pengujian ini.
