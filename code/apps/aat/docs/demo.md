# Demo manual AAT

Jalankan stack mengikuti [README](../README.md), lalu jalankan perintah berikut
**dari direktori `code/apps/aat`**. Memerlukan `curl` dan `jq`.

```sh
set -a
. ./.env
set +a
export BMKG_URL="http://localhost:${BMKG_PORT:-8081}"
export PVMBG_URL="http://localhost:${PVMBG_PORT:-8082}"
export AGGREGATOR_URL="http://localhost:${AGGREGATOR_PORT:-8083}"
```

Server Aggregator bagian 1 menerima batch lewat HTTP dan menyediakan data lewat
HTTP. Pengambilan otomatis dari mock akan dipasang sebagai polling bagian 3.
Untuk memverifikasi bagian 1 secara manual, ekspor `.env` dan URL seperti di atas.

```sh
correlation_id="manual-$(date +%s)"
# Ambil kedua endpoint BMKG lalu kirim satu batch ke Aggregator.
demo_dir=$(mktemp -d)
curl -fsS -H "X-BMKG-Key: $BMKG_API_KEY" -H "X-Correlation-ID: $correlation_id" \
  "$BMKG_URL/seismic-events" > "$demo_dir/events.json"
curl -fsS -H "X-BMKG-Key: $BMKG_API_KEY" -H "X-Correlation-ID: $correlation_id" \
  "$BMKG_URL/tsunami-warnings" > "$demo_dir/warnings.json"
jq -n --slurpfile e "$demo_dir/events.json" --slurpfile w "$demo_dir/warnings.json" \
  '{seismic_events:$e[0],tsunami_warnings:$w[0]}' | \
  curl -fsS -H "Authorization: Bearer $AGGREGATOR_TOKEN" \
    -H "X-Correlation-ID: $correlation_id" -H 'Content-Type: application/json' \
    --data-binary @- "$AGGREGATOR_URL/internal/ingest/bmkg"
rm "$demo_dir/events.json" "$demo_dir/warnings.json"
rmdir "$demo_dir"

# Skema PVMBG awal: ambil data dan kirim batch lewat HTTP.
curl -fsS -H "Authorization: Bearer $PVMBG_TOKEN" -H 'Content-Type: application/json' \
  -d '{"enabled":false}' "$PVMBG_URL/admin/schema-version"
curl -fsS -H "Authorization: Bearer $PVMBG_TOKEN" -H "X-Correlation-ID: $correlation_id" \
  "$PVMBG_URL/volcanic-reports" | jq '{volcanic_reports:.}' | \
  curl -fsS -H "Authorization: Bearer $AGGREGATOR_TOKEN" \
    -H "X-Correlation-ID: $correlation_id" -H 'Content-Type: application/json' \
    --data-binary @- "$AGGREGATOR_URL/internal/ingest/pvmbg"

# Aktifkan schema baru tanpa restart; hanya ingest laporan baru setelah batas waktu.
since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl -fsS -H "Authorization: Bearer $PVMBG_TOKEN" -H 'Content-Type: application/json' \
  -d '{"enabled":true}' "$PVMBG_URL/admin/schema-version"
sleep 11
curl -fsS -G --data-urlencode "since=$since" \
  -H "Authorization: Bearer $PVMBG_TOKEN" -H "X-Correlation-ID: $correlation_id" \
  "$PVMBG_URL/volcanic-reports" | jq '{volcanic_reports:.}' | \
  curl -fsS -H "Authorization: Bearer $AGGREGATOR_TOKEN" \
    -H "X-Correlation-ID: $correlation_id" -H 'Content-Type: application/json' \
    --data-binary @- "$AGGREGATOR_URL/internal/ingest/pvmbg"
curl -fsS -H "Authorization: Bearer $AGGREGATOR_TOKEN" \
  "$AGGREGATOR_URL/internal/hazards?source=PVMBG&limit=1000" | jq .
# Periksa attributes: record lama tanpa confidence_level, baru dengan field itu.

# Outage aktual: PVMBG 503, BMKG dan data tersimpan tetap bisa dibaca.
curl -fsS -H "Authorization: Bearer $PVMBG_TOKEN" -H 'Content-Type: application/json' \
  -d '{"enabled":true}' "$PVMBG_URL/admin/outage"
curl -i -H "Authorization: Bearer $PVMBG_TOKEN" "$PVMBG_URL/volcanic-reports"
curl -i -H "X-BMKG-Key: $BMKG_API_KEY" "$BMKG_URL/seismic-events"
curl -fsS -H "Authorization: Bearer $AGGREGATOR_TOKEN" "$AGGREGATOR_URL/internal/hazards"
curl -fsS -H "Authorization: Bearer $PVMBG_TOKEN" -H 'Content-Type: application/json' \
  -d '{"enabled":false}' "$PVMBG_URL/admin/outage"
curl -i -H "Authorization: Bearer $PVMBG_TOKEN" "$PVMBG_URL/volcanic-reports"
# Kredensial silang harus menghasilkan 401.
curl -i -H "X-BMKG-Key: $PVMBG_TOKEN" "$BMKG_URL/seismic-events"
curl -i -H "Authorization: Bearer $BMKG_API_KEY" "$PVMBG_URL/volcanic-reports"
# Kembalikan schema awal.
curl -fsS -H "Authorization: Bearer $PVMBG_TOKEN" -H 'Content-Type: application/json' \
  -d '{"enabled":false}' "$PVMBG_URL/admin/schema-version"
```

Simpan output dan log correlation ID sebagai bukti demo. Alur manual ini belum
membuktikan polling otomatis; integrasi worker tetap menjadi bagian 3.

