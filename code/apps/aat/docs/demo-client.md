# Demo Akses Client dan Keamanan

Jalankan dari `code/apps/aat`. Bagian 1 menyediakan mock, Aggregator, PostgreSQL,
dan `.env`. Python 3 diperlukan untuk script persiapan dan demo. Ketentuan identitas,
field, refresh, dan batas integrasi dijelaskan di [kontrak client](client-contract.md).

## Persiapan

```sh
python3 scripts/setup-client-env.py
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml up -d --build
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml ps
```

Script persiapan membuat empat secret acak di `.env`, mengganti placeholder bagian
2, dan mempertahankan kredensial yang sudah diisi. `.env` diabaikan Git dan hanya
dapat dibaca/ditulis pemilik file. Secret mock, Aggregator, Auth internal, dan
ketiga identitas harus berbeda. Auth/Client API tidak mengakses PostgreSQL langsung.

Ingest data terlebih dahulu mengikuti [demo bagian 1](demo.md). Setelah ada data
BMKG dalam Canonical Store, jalankan:

```sh
python3 scripts/demo-client.py --wait-expiry
```

Script menunggu token Tim Lapangan kedaluwarsa alami (default 60 detik),
lalu refresh tanpa login ulang dan menolak token lama. Script juga menguji
login tiga identitas, allowlist field, refresh rotation, penolakan
token lama, pencabutan session akibat replay, auth salah, serta kontrak mock dengan
kredensial valid dan silang. Output hanya berisi nama field/status, tanpa token.
Script meminta token baru sehingga menggunakan tiga slot session per eksekusi;
session kedaluwarsa otomatis setelah satu jam.

## Coba login dan membaca data

Contoh berikut memerlukan `curl` dan `jq`. Jalankan pada shell lokal dari direktori
AAT. Secret berasal dari `.env`; jangan menyalin token ke laporan atau commit.

```sh
set -a
. ./.env
set +a
auth_url="http://localhost:${AUTH_PORT:-8084}"
client_url="http://localhost:${CLIENT_PORT:-8080}"

tokens=$(jq -n --arg secret "$PUBLIC_CLIENT_SECRET" \
  '{client_id:"public",client_secret:$secret}' | \
  curl -fsS -H 'Content-Type: application/json' --data-binary @- "$auth_url/auth/token")
access=$(printf '%s' "$tokens" | jq -r .access_token)
refresh=$(printf '%s' "$tokens" | jq -r .refresh_token)
curl -fsS -H "Authorization: Bearer $access" "$client_url/hazards?limit=5" | jq .

tokens=$(jq -n --arg token "$refresh" '{refresh_token:$token}' | \
  curl -fsS -H 'Content-Type: application/json' --data-binary @- "$auth_url/auth/refresh")
access=$(printf '%s' "$tokens" | jq -r .access_token)
refresh=$(printf '%s' "$tokens" | jq -r .refresh_token)
curl -fsS -H "Authorization: Bearer $access" "$client_url/hazards?source=BMKG&limit=5" | jq .
```

Gunakan `client_id: "responder"` dengan `RESPONDER_CLIENT_SECRET`, atau `analyst`
dengan `ANALYST_CLIENT_SECRET`, untuk melihat perbedaan field. Role tidak bisa
ditingkatkan lewat body login maupun query API. Refresh token hanya dapat dipakai
sekali; replay mencabut seluruh session. Access token default berlaku 60 detik,
refresh maksimum satu jam. Auth restart menghapus session dan memerlukan login ulang.

Untuk membuktikan penolakan field Mentah pada Media, setelah login public:

```sh
curl -i -H "Authorization: Bearer $access" "$client_url/hazards?fields=attributes,latitude"
# Harus HTTP 403. Field ringkasan boleh diminta secara eksplisit:
curl -fsS -H "Authorization: Bearer $access" "$client_url/hazards?fields=source,severity"
```

## Membuktikan concurrency dan status stale

```sh
go test -race ./auth/... ./client/... ./internal/httpkit
go test ./bmkg/... ./pvmbg/...
go vet ./...
```

Tes `TestConcurrentRequestRejectedAndTimeoutReleasesSlot` menahan upstream,
memastikan request kedua langsung 429 dengan `Retry-After`, lalu memastikan slot
kembali tersedia setelah timeout. Tes refresh bersamaan memastikan token hanya
berhasil digunakan sekali. Ini merupakan tes batas API; load test seluruh sistem dikoordinasikan secara terpisah.

Tes freshness memakai waktu polling sukses yang baru/lama, polling gagal, serta
status hilang/rusak. Pada stack sekarang, endpoint status belum tersedia,
sehingga respons nyata menampilkan `unknown` dan `stale: true`. Jangan menganggap
umur event atau keberhasilan manual ingest sebagai waktu polling terakhir.

## Konfigurasi dan operasional

| Variabel                    | Default         | Arti                                              |
| --------------------------- | --------------- | ------------------------------------------------- |
| `AUTH_PORT` / `CLIENT_PORT` | `8084` / `8080` | Port host localhost                               |
| `ACCESS_TTL_SECONDS`        | `60`            | Umur access token                                 |
| `REFRESH_TTL_SECONDS`       | `3600`          | Umur maksimum session, tidak diperpanjang refresh |
| `AUTH_MAX_SESSIONS`         | `1000`          | Batas session Auth per proses                     |
| `AUTH_MAX_CONCURRENT`       | `32`            | Batas request Auth aktif per proses               |
| `CLIENT_MAX_CONCURRENT`     | `16`            | Batas request Client API aktif per proses         |
| `UPSTREAM_TIMEOUT_MS`       | `3000`          | Batas waktu total panggilan upstream per request  |
| `STALE_AFTER_SECONDS`       | `60`            | Ambang waktu sejak polling sukses terakhir        |

Semua batas harus positif. Refresh TTL harus minimal sebesar access TTL. Respons
401 berarti kredensial/token tidak sah; 429 berarti kapasitas request/session penuh;
503 berarti Auth atau pembacaan data Aggregator gagal. Kegagalan endpoint status
saja menghasilkan data tersimpan dengan status unknown.

```sh
# Lihat log layanan bagian 2.
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml logs --tail=50 auth client
# Hentikan layanan bagian 2 saja.
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml stop auth client
# Hentikan seluruh stack, pertahankan data PostgreSQL.
docker compose -p aat-part1 -f compose.yaml -f compose.client.yaml down
```

Overlay ini membantu demo lokal akses client. Orkestrasi penuh, pencatatan polling, dan load test terintegrasi dikelola secara terpisah. Broker dan dua consumer tersedia pada stack Compose dasar. Untuk deployment beberapa replica Auth diperlukan penyimpanan session bersama; TLS diperlukan jika layanan dibuka di luar localhost.
