"""Prepare local M1 configuration without printing or rotating existing secrets.
"""
import argparse
from datetime import datetime, timezone
from pathlib import Path
import secrets

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--env-file', type=Path, default=root / '.env')
args = parser.parse_args()
path = args.env_file
lines = (path if path.exists() else root / '.env.example').read_text().splitlines()
keys = ('BMKG_API_KEY', 'PVMBG_TOKEN', 'AGGREGATOR_TOKEN', 'POSTGRES_PASSWORD',
        'AUTH_INTERNAL_TOKEN', 'PUBLIC_CLIENT_SECRET', 'RESPONDER_CLIENT_SECRET', 'ANALYST_CLIENT_SECRET')
values, positions = {}, {}
for index, line in enumerate(lines):
    if line.strip() and not line.lstrip().startswith('#') and '=' in line:
        key, value = line.split('=', 1)
        key = key.strip()
        if key in values:
            raise SystemExit(f'Duplicate configuration key: {key}')
        values[key] = value.strip().strip('\"\'')
        positions[key] = index
changed = 0
for key in keys:
    value = values.get(key, '')
    if not value or value.startswith('example-'):
        value = secrets.token_hex(32)
        values[key] = value
        if key in positions:
            lines[positions[key]] = f'{key}={value}'
        else:
            lines.append(f'{key}={value}')
        changed += 1
    if len(value) < 32 or any(c.isspace() for c in value):
        raise SystemExit(f'{key} must be at least 32 characters without whitespace.')
if len({values[key] for key in keys}) != len(keys):
    raise SystemExit('Use a distinct credential for each domain and client.')
start_added = not values.get('START_TIME')
if start_added:
    start = datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')
    if 'START_TIME' in positions:
        lines[positions['START_TIME']] = f'START_TIME={start}'
    else:
        lines.append(f'START_TIME={start}')
if changed or start_added or not path.exists():
    # Create with owner-only permissions before writing secrets.
    path.touch(mode=0o600)
    path.chmod(0o600)
    path.write_text('\n'.join(lines) + '\n')
else:
    path.chmod(0o600)
print(f'Configuration ready; {changed} credentials generated. Existing credentials preserved.')
print('Start time initialized.' if start_added else 'Existing start time preserved.')
