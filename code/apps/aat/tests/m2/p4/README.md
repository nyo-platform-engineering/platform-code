# P4 — test deployment cluster

```sh
cd code/infrastructure
task m2:p4
```

Deploy Aggregator dan Client dari PR ini terlebih dahulu. Runner mengecek
capability sebelum mengubah deployment. Butuh kubeconfig lab, `.env.runtime`,
credential analyst, dan akses `kubectl exec` ke Aggregator serta coordinator Citus.

| Kriteria | Pengujian |
| --- | --- |
| 1–3 | SSE terkontrol: dua satelit → satu kebakaran; event time; batas terlambat 6 jam tepat dan lewat 1 menit; update FRP/confidence; shrink, retract, dissolve, revive dengan ID yang sama. |
| 4 | Feed benar-benar mengirim 20 → **1.200 event/detik (60×)** selama 10 detik. Cek offset akhir, semua deteksi, oracle pasangan, recovery ≤60 detik sejak burst berakhir, p95 setiap event, dan lag pada API dashboard Client yang ter-deploy. |
| 5 | Tahan transaksi setelah penulisan HazardEvent sebelum commit offset, hentikan mendadak **proses owner yang ter-deploy**, lalu bandingkan seluruh state dengan run tanpa gangguan pada offset akhir identik. |
| 6 | Referensi asisten: pending sampai diberikan. Checksum internal bukan penggantinya. |
| 7 | Observasi feed live pada deployment selama 180 detik. |
| 8 | Putus koneksi SSE secara sengaja, replay offset inklusif, pastikan state tidak berubah/ganda. |

Seluruh skenario acceptance P4 dijalankan dari folder ini. Dukungan runtime di
Aggregator (`p4_verification.go` dan route internal) mengisolasi schema dan
menyediakan fault injection; itu bukan suite tes tambahan. Route Client
`/satellite/stats` menyediakan data untuk dashboard yang ter-deploy.

**Target tetap deployment Aggregator, Client, dan Citus yang sama.** Hanya sumber
SSE fixture yang ditambahkan. Data terkontrol memakai schema kosong sementara pada
database Citus yang sama, dengan tabel HazardEvent tetap didistribusikan ke worker.
Hasil performa berlaku untuk ukuran fixture yang dicatat, bukan sertifikasi semua
ukuran dataset atau hasil referensi resmi.

Selama bagian terkontrol, dashboard menampilkan data verifikasi. Source lain,
relay notifikasi, dan pemroses Field Report dimatikan pada mode ini; API menolak
write lain. Jalankan hanya di lab dan jangan bersamaan dengan tes lain.
Env Aggregator, feed live, dan Argo dipulihkan saat selesai/Ctrl+C. Schema dan
sumber sementara dibuang **setelah** deployment asli pulih. Data live tidak dihapus.

Jika terminal/proses runner mati mendadak, pulihkan dari journal privat:

```sh
task m2:p4 -- --restore /path/evidence/restore-p4.json
```

Evidence utama: `deployment.json`, `controlled-event-time-corrections.json`,
`controlled-burst.json`, `controlled-burst-progress.json`,
`controlled-burst-latencies.json`, `uninterrupted-state.json`,
`recovered-state.json`, `controlled-crash-exit.json`, `controlled-crash-restart.json`,
`controlled-crash-equivalence.json`,
`controlled-reconnect.json`, dan `restore-result.json`.
P95 memakai receipt SSE → commit durable setiap event, termasuk hasil HazardEvent
dalam batch tersebut. Nilai maksimum per batch dari image lama hanya estimasi.

Untuk kriteria 6, isi `CONFIG.reference_file` dengan adapter dari data asisten:
`origin=assistant`, `source=SATELIT`, `through_offset`, `count`, `checksum`,
`canonicalization=aggregator-audit-checksum-v1`. Gunakan deklarasi canonicalization
itu hanya jika format hash benar-benar sama. Runner membandingkan pada offset
identik dan menyimpan selisih; tanpa referensi, status tetap incomplete/pending.

Crash test merekam exit container owner asli segera setelah fault. Restart startup
berikutnya tidak menghapus bukti yang sudah direkam. Exit selain 137 atau bukti
exit asli yang sudah hilang menggagalkan tes langsung; menunggu Pod Ready tidak
mengubah kegagalan tersebut menjadi kondisi yang akan pulih sendiri.
