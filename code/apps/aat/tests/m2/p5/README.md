# P5 — Sensor Pantai dan game day

```sh
cd code/infrastructure
task m2:p5
```

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
`critical-receipts.json`, dan `cleanup.json`. Error cleanup tidak menutupi penyebab
kegagalan skenario.

Target tetap 99% availability request Tim Lapangan dan p95 alert ≤5 detik.
Availability request dan latensi pengiriman alert adalah ukuran berbeda;
kelulusan probe HTTP tidak menggantikan bukti penerimaan semua event kritis.
Run practice merupakan rehearsal, bukan sertifikasi profil ujian/Tester Agent.
