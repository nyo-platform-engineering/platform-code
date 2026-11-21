# P5 — Sensor Pantai dan game day

```sh
cd code/infrastructure
task m2:p5
```

Jangan jalankan bersamaan dengan P4 atau suite lain yang mengubah deployment.
Setup menolak mode verifikasi P4 yang masih aktif. Runner menahan rekonsiliasi
seluruh Application AAT yang dipakai, termasuk Client dan dependencies, lalu
menunggu rollout Deployment/StatefulSet selesai sebelum reset dan pengukuran.
Status Argo dipulihkan dari `setup-baseline.json` saat selesai/gagal.

Deploy image Aggregator dan chart terbaru sebelum mengulang game day:

- Client: 3 replika tersebar di tiga node. NodePort melayani replika lokal;
  akses entrypoint pada node yang hidup. NodePort pada node mati tetap tidak tersedia.
- Citus coordinator: sidecar `metadata-reconciler` menyinkronkan metadata worker
  lewat API Citus setiap 5 detik bila diperlukan. Ini komponen runtime yang sudah
  berjalan sebelum fault, bukan tindakan runner selama pemulihan.
- Aggregator: retry error startup sementara saat metadata belum sinkron, dalam
  deadline startup yang sama. Error skema permanen tetap gagal.

Konfigurasi koneksi Citus membatasi socket worker yang macet. Checksum ConfigMap
memicu rollout storage saat konfigurasi berubah; tunggu deployment/rollout selesai
sebelum tes, di luar window pemulihan.

Runner mengakhiri semua fault sebelum mencatat `hands-off`. Setelah itu hanya
probe baca dan pemeriksaan health; tidak ada restart, scale, atau perbaikan
metadata oleh runner. Kegagalan saat mengakhiri fault tetap menggagalkan run.

Health mensyaratkan seluruh replika siap, sumber sehat, jawaban lengkap, metadata
primary Citus sinkron, serta outbox dan consumer critical kosong. Evidence:
`gameday-health.json`, `gameday-combined.json`, `gameday-combined-probe.jsonl.json`,
`critical-receipts.json`, `setup-ready.json`, dan `cleanup.json`.
Kegagalan jaringan juga dicatat di `http.jsonl` bersama endpoint, durasi, dan
alasan OS (misalnya connection refused atau timeout); kegagalan NodePort tetap
menggagalkan pengukuran, tanpa dialihkan ke port-forward. Error cleanup tidak menutupi penyebab
kegagalan skenario.

Target tetap 99% availability request Tim Lapangan dan p95 alert ≤5 detik.
Availability request dan latensi pengiriman alert adalah ukuran berbeda;
kelulusan probe HTTP tidak menggantikan bukti penerimaan semua event kritis.
Run practice merupakan rehearsal, bukan sertifikasi profil ujian/Tester Agent.

P5 mengukur availability melalui NodePort publik Client. Jika `/health` pada
entrypoint itu tidak dapat dijangkau, setup gagal sebelum fault; runner tidak
menggantinya dengan port-forward yang terikat ke satu Pod. Endpoint pengukuran
tersimpan di `gameday-endpoint.json`. Penggantian tunnel internal diserialkan agar
thread Media tidak saling mematikan koneksi tester. Kegagalan request tetap
dicatat dan tidak dihapus dari hasil SLO.

Jika fault gagal diakhiri, window recovery belum dimulai: run gagal sebagai
skenario tidak valid, bukan diklaim sebagai layanan gagal pulih selama 150 detik.
Angka probe tetap disimpan untuk diagnosis, tetapi bukan hasil SLO game day sah.

| Kriteria | Bukti yang tetap diperlukan |
| --- | --- |
| 1 | Seluruh 3 critical reading practice punya receipt durable; 0 hilang, p95 ≤5 detik. |
| 2 | Mapping/akses Ops diperiksa; seluruh pelanggaran kontrak harus dibuktikan terpisah, tidak dianggap lulus hanya karena daftar parked kosong. |
| 3 | P95 setiap sumber lain saat sensor outage tetap <300 ms. |
| 4 | Back-off request, jawaban cepat dengan status stale, lalu breaker pulih sendiri. |
| 5 | Media mendapat 429 terkontrol, request Tim Lapangan tetap terlayani. |
| 6 | Semua fault diakhiri; setelah `hands-off` runner hanya membaca. Recovery harus terbukti lewat endpoint publik. Rehearsal tidak menggantikan profil ujian/Tester Agent. |
| 7 | Rekaman dan penjelasan dashboard, metrik, serta trace tetap wajib sebagai evidence manual. |
| 8 | Laporkan SLO/error budget dari run yang valid. Availability HTTP 99% terpisah dari SLI pengiriman alert; receipt/p95 alert game day tetap harus dilaporkan, bukan digantikan angka HTTP. |

Pada skenario gabungan, outage PVMBG disimulasikan dengan NetworkPolicy ingress
yang memutus akses ke Pod PVMBG. Pencabutan fault memakai API control-plane,
tidak memerlukan port-forward ke kubelet pada node yang baru dinyalakan.
Boot node dan pulihnya PVMBG tetap diamati setelah `hands-off`; runner tidak
menunggu Pod sehat sebagai syarat pencabutan fault. Ini menguji PVMBG tidak
terjangkau, bukan respons HTTP 503 dari flag admin mock. Cleanup flag admin tetap
tersedia pada `--restore` untuk run lama yang memakai flag tersebut.
