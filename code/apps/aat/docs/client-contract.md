# Kontrak akses client

## Identitas dan field

Ketiga identitas menggunakan `client_id` dan secret berbeda. Hak akses ditentukan
server; client tidak dapat memilih role saat meminta atau me-refresh token.

| client_id                             | Field HazardEvent yang terlihat                                                                                    |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| `public` — Media / Pers               | Tujuh field Ringkasan: `hazard_id`, `source`, `hazard_type`, `severity`, `area_name`, `occurred_at`, `ingested_at` |
| `responder` — Tim Lapangan            | Seluruh sebelas field, termasuk `source_ref_id`, `latitude`, `longitude`, `attributes`                             |
| `analyst` — BNPB Pusat / Internal Ops | Seluruh sebelas field, sama dengan Tim Lapangan                                                                    |

Semua identitas dapat membaca kedua sumber. Filter menggunakan allowlist di
Client API: field baru dari Aggregator tidak otomatis menjadi publik. Envelope
`next_after` dan `sources` tersedia untuk semua identitas. Field `attributes`
beserta seluruh isi dinamisnya boleh diakses responder dan analyst.

## Auth Service

- `POST /auth/token`: JSON `{"client_id":"public","client_secret":"..."}`.
- `POST /auth/refresh`: JSON `{"refresh_token":"..."}`.
- Keduanya mengembalikan `access_token`, `token_type: "Bearer"`, `expires_in`,
  `refresh_token`, dan `refresh_expires_in`; `Cache-Control: no-store`.
- Access token opaque berlaku 60 detik secara default (configurable lewat `ACCESS_TTL_SECONDS`); refresh session maksimum 1 jam. Refresh
  merotasi kedua token dan membatalkan access token lama. Penggunaan ulang refresh
  token lama mencabut seluruh session, termasuk pasangan token hasil rotasi.
  Maksimum 127 kali refresh per session; setelahnya client perlu login ulang.
- Secret salah/token kedaluwarsa/tidak dikenal menghasilkan 401. Kapasitas session
  atau batas request bersamaan tercapai menghasilkan 429 dan `Retry-After: 1`.
- Token acak disimpan sebagai hash. Session bersifat in-memory pada satu Auth
  Service; restart mengharuskan login ulang. Untuk replica, diperlukan shared store.
- `POST /internal/introspect`: Bearer `AUTH_INTERNAL_TOKEN` dan JSON
  `{"token":"..."}`. Respons `{"active":true,"client_id":"public"}` atau
  `{"active":false}`. Client API selalu memeriksa token ke endpoint ini.
- `/health` publik. Endpoint lainnya menggunakan body maksimal 16 KiB.

## Client-Facing API

`GET /hazards` memerlukan Bearer access token. Query yang diteruskan ke Aggregator:
`source=BMKG|PVMBG`, `limit=1..1000`, dan `after`. Query `fields` menerima
nama field dipisahkan koma dan diproses di Client API. Permintaan eksplisit field
Mentah oleh Media menghasilkan 403 sebelum pembacaan data, termasuk kombinasi
Ringkasan/Mentah. Field tak dikenal juga 403; query lain atau sintaks salah 400.
Semua client read-only: tidak ada endpoint tulis data client pada M1.
Tidak ada akses database langsung dari Client API atau Auth Service.

`CLIENT_MAX_CONCURRENT` membatasi request aktif per proses, termasuk panggilan
Auth/Aggregator. Slot penuh langsung ditolak dengan 429 dan `Retry-After: 1`;
tidak ada antrean tak terbatas. `AUTH_MAX_CONCURRENT` menerapkan batas serupa pada
Auth Service. Batas ini bukan rate limit per pengguna ataupun batas lintas replica.

Token tidak sah menghasilkan 401. Auth atau pembacaan hazards tidak tersedia,
timeout, atau respons upstream rusak menghasilkan 503. Token internal tidak
pernah diteruskan ke client. Timeout dan pembatalan request diteruskan ke upstream.
Setiap panggilan keluar mencatat JSON log `outbound_request` dengan correlation ID,
tujuan/path, status (0 jika transport gagal), dan latensi termasuk pembacaan body.
Secret, token, query, dan isi body tidak dicatat.

## Integrasi Mock dan Aggregator

Kontrak autentikasi mock yang sudah ada dipertahankan dan diuji lintas kredensial:

| Tujuan                                      | Kredensial                               |
| ------------------------------------------- | ---------------------------------------- |
| BMKG `/seismic-events`, `/tsunami-warnings` | `X-BMKG-Key: BMKG_API_KEY`               |
| PVMBG `/volcanic-reports`, `/admin/*`       | `Authorization: Bearer PVMBG_TOKEN`      |
| Aggregator `/internal/*`                    | `Authorization: Bearer AGGREGATOR_TOKEN` |

Ketiga nilai harus berbeda. Client API hanya memegang token Aggregator dan token introspeksi Auth; tidak memegang key mock. Credential client tidak berlaku pada mock maupun Aggregator. /health tetap publik sesuai implementasi sistem.

## Penyajian Status Stale

Sistem menyediakan GET /internal/source-status pada Aggregator, dilindungi token Aggregator yang sama. Respons contoh:

```json
{
  "sources": [
    { "source": "BMKG", "healthy": true, "last_success_at": "2026-09-30T10:00:00Z" },
    { "source": "PVMBG", "healthy": false, "last_success_at": null }
  ]
}
```

`healthy` berarti hasil polling terakhir, dan `last_success_at` adalah waktu
awal panggilan polling terakhir yang berhasil, dicatat dari `now()` pada caller
sebelum mengambil data. Nilai ini juga menjadi `since` berikutnya, termasuk bila
polling sukses tanpa data baru. Jangan memakai
`occurred_at`, `ingested_at`, atau health check HTTP sebagai penggantinya.

Client API menyajikan `sources` dengan `source`, `status`, `last_success_at`, dan
`stale`. Status `fresh` hanya jika healthy=true dan waktu sukses tidak lebih tua
dari `STALE_AFTER_SECONDS` (default 60). Status `stale` jika polling gagal atau
timestamp terlalu tua. Status `unknown` dan stale=true jika endpoint belum ada,
request gagal, entri hilang/rusak, timestamp kosong, atau timestamp di masa depan.
Kegagalan status tidak menghalangi pembacaan hazards yang sudah tersimpan.

Endpoint status dan worker pencatatnya dikelola secara terpisah. Pada stack dasar, status unknown adalah hasil yang diharapkan sampai endpoint tersebut terpasang.

## Batas demo

Transport HTTP dibatasi localhost untuk demo. Penempatan di luar mesin lokal memerlukan TLS. Polling dan broker/consumer tersedia di Aggregator dan Compose; koordinasi load test sistem tetap perlu dilakukan. Pengujian sistem mencakup penolakan concurrency dengan upstream tertahan.
