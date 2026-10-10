# Load testing M1 P2.2

Skrip k6 adalah `scripts/load/test2.js` (sebelumnya `m1.js`), khusus Problem 2.
Untuk seluruh kriteria Problem 2, jalankan `python3 tests/test-problem-2.py`;
petunjuk ada di [panduan tests](../tests/README.md).

Pengujian memakai k6 lokal dan seluruh stack Docker: Client API, Auth, Aggregator,
PostgreSQL, kedua mock, NATS dan dua consumer. Producer tetap polling dan
mempublikasikan event selama load. Hasil ini adalah hasil mesin lokal satu
instance, bukan kapasitas deployment produksi.

## Mengulang dari kondisi awal

Dari `code/apps/aat`, setelah Docker aktif dan k6 terpasang:

```sh
python3 scripts/setup-env.py
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml up -d --build
# Tunggu kedua source selesai polling pertama sebelum menjalankan test.
python3 scripts/load-test.py --project aat-part1 --vus 50 --seconds 65 \
  --output-dir docs/evidence/load-run-baru
```

Output directory harus baru agar bukti lama tidak tertimpa. Konfigurasi project
terisolasi dapat dipakai dengan `--env-file PATH --project NAMA`. Script tidak
memicu outage, mengubah konfigurasi aplikasi, atau menghentikan container.

Tiap VU mempunyai session terpisah, bergantian memakai identitas Media,
Tim Lapangan dan BNPB Internal. Login dijalankan pada setup sebelum pengukuran.
Request yang diukur adalah `GET /hazards?source=BMKG|PVMBG&limit=100`, dengan
kedua sumber bergantian dan autentikasi nyata Client->Auth->Aggregator->DB.
Request 200 diperiksa: data tidak kosong, source benar, field sesuai identitas,
serta correlation ID sesuai. Login dan refresh tidak masuk throughput/latensi
hazards; jumlah refresh dan penolakan refresh disajikan terpisah.

TTL token tetap 60 detik. Masing-masing VU me-refresh token sekitar 10 detik
sebelum expiry; pengujian expiry alami sudah dilakukan oleh demo client terpisah.
Kredensial hanya diteruskan melalui environment subprocess. File summary k6
hanya menyimpan metrics/state/options, tidak menyimpan setup data/token.

## Pola beban dan definisi hasil

Executor [constant-vus](https://grafana.com/docs/k6/latest/using-k6/scenarios/executors/constant-vus/)
menjaga 50 VU selama 65 detik. Masing-masing VU mengirim request berurutan tanpa
think time dan memakai HTTP keep-alive. Ini closed workload dengan 50 koneksi
paralel, bukan target arrival rate tertentu. Graceful stop maksimum 10 detik
membiarkan request terakhir selesai; 65 detik adalah fase sustained, di luar setup.

429 memiliki `Retry-After: 1`. Pada pengujian saturasi ini hazard VU sengaja tidak
menunggu Retry-After agar pressure tetap penuh. Karena request yang ditolak cepat,
VU dapat menghasilkan sangat banyak 429. Pola ini perlu disebutkan dalam laporan;
client produksi sebaiknya menghormati Retry-After/backoff.

- **Throughput total:** semua request hazards / 65 detik.
- **Throughput sukses:** respons 200 dengan body yang valid / 65 detik.
- **Penolakan terkontrol:** hanya HTTP 429 dengan Retry-After >=1.
- **Error rate utama:** unexpected failures / (semua hazards - controlled 429).
  Ini lebih ketat dibanding memakai semua request sebagai denominator.
- **Latensi:** p50/p95/p99 durasi HTTP k6 (send+wait+receive), ditampilkan terpisah
  untuk semua respons dan hanya respons sukses. Penolakan cepat tidak boleh
  dipakai untuk menyatakan pembacaan data secepat p95 gabungan.
- **Sustained:** jumlah request/sukses disimpan untuk tiap jendela 5 detik,
  termasuk jendela 60-65 detik; pengujian gagal jika suatu jendela tidak menerima
  request/sukses. Minimal 50 request per jendela dipakai sebagai sanity check.
- **Ketahanan:** container ID, Running/OOMKilled/RestartCount, /health Client,
  dan koneksi TCP API diperiksa berkala. Polling source dibandingkan sebelum/sesudah.

Lulus jika threshold k6 lulus, fase sustained mencakup seluruh durasi,
>=50 koneksi TCP API teramati, tidak ada crash/restart/OOM, health tetap baik,
polling tetap maju dan unexpected error rate <1%. Kondisi Auth refresh gagal
juga membuat pengujian gagal.

## Konfigurasi audit

Konfigurasi aplikasi default dipertahankan: Client concurrency 16, Auth 32,
pool DB 5, access TTL 60 detik, poll interval 3 detik, timeout poll 4 detik,
timeout Client upstream 3 detik. PVMBG min=max=3000 ms untuk mempertahankan
sumber lambat. Tiga identitas dan kedua sumber diuji dengan page limit 100.

Laporan hasil terukur dan bukti final tersedia di
[hasil load test](evidence/load-verified/report.md). Run pertama pada
`evidence/load-normal/` dipertahankan sebagai hasil awal; sampel
`tcp-connections.json` pada run pertama diambil **setelah test selesai**, sehingga
bernilai nol. Bukti TCP selama load direkam otomatis pada result run final.

## Untuk laporan M1

Gunakan tabel hasil, konfigurasi, definisi error rate/latensi, jumlah 429,
pembahasan batas konkurensi, dan catatan bahwa generator serta server memakai
mesin yang sama. Bandingkan throughput sukses dengan throughput total, lalu
jelaskan mengapa bounded concurrency menjaga ketersediaan di bawah saturasi.
Angka hasil tidak membuktikan semua request diterima; penolakan terkontrol yang
besar menunjukkan kebutuhan tuning/scaling bila targetnya acceptance lebih tinggi.
