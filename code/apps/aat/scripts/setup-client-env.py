"""Add part-2 secrets to the local .env without printing them or rotating real secrets."""
from pathlib import Path
import secrets

root = Path(__file__).resolve().parents[1]
path = root / ".env"
if not path.exists():
    raise SystemExit("Buat .env bagian 1 terlebih dahulu mengikuti README AAT.")

lines = path.read_text().splitlines()
values = {}
positions = {}
for index, line in enumerate(lines):
    if line.strip() and not line.lstrip().startswith("#") and "=" in line:
        key, value = line.split("=", 1)
        key = key.strip()
        if key in values:
            raise SystemExit(f"Key .env duplikat: {key}")
        values[key] = value.strip().strip("\"'")
        positions[key] = index

keys = ("AUTH_INTERNAL_TOKEN", "PUBLIC_CLIENT_SECRET", "RESPONDER_CLIENT_SECRET", "ANALYST_CLIENT_SECRET")
changed = 0
for key in keys:
    value = values.get(key, "")
    if not value or value.startswith("example-"):
        value = secrets.token_hex(24)
        values[key] = value
        if key in positions:
            lines[positions[key]] = f"{key}={value}"
        else:
            lines.append(f"{key}={value}")
        changed += 1
    if len(value) < 32 or any(character.isspace() for character in value):
        raise SystemExit(f"{key} harus minimal 32 karakter tanpa whitespace.")

seen = set()
for key in (*keys, "BMKG_API_KEY", "PVMBG_TOKEN", "AGGREGATOR_TOKEN"):
    value = values.get(key, "")
    if not value or value.startswith("example-") or value in seen:
        raise SystemExit(f"Periksa {key}: wajib terisi, unik, dan bukan contoh.")
    seen.add(value)

path.chmod(0o600)
if changed:
    path.write_text("\n".join(lines) + "\n")
print(f".env siap; {changed} kredensial bagian 2 dibuat. Nilai tidak ditampilkan.")
