#!/usr/bin/env python3
"""Run after docker compose up. Press Enter once to change the PVMBG schema."""

import datetime as dt
import json
from pathlib import Path
import sys
import time
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
ENV = {}
PORTS = {'bmkg': '8081', 'pvmbg': '8082', 'aggregator': '8083'}
VOLCANOES = {
    'MERAPI': ('Gunung Merapi', -7.54, 110.446),
    'SEMERU': ('Gunung Semeru', -8.108, 112.922),
    'ANAK_KRAKATAU': ('Gunung Anak Krakatau', -6.102, 105.423),
}


def load_env():
    for line in (ROOT / '.env').read_text().splitlines():
        line = line.strip()
        if not line or line.startswith('#') or '=' not in line:
            continue
        key, value = line.split('=', 1)
        ENV[key.strip()] = value.strip().strip("\"'")


def get_time(value):
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00'))


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def request(service, endpoint, body=None, params=None):
    port = ENV.get(service.upper() + '_PORT', PORTS[service])
    url = f'http://127.0.0.1:{port}{endpoint}'
    if params:
        url += '?' + urllib.parse.urlencode(params)

    if service == 'bmkg':
        headers = {'X-BMKG-Key': ENV['BMKG_API_KEY']}
    else:
        token = ENV[service.upper() + '_TOKEN']
        headers = {'Authorization': 'Bearer ' + token}

    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers['Content-Type'] = 'application/json'

    req = urllib.request.Request(url, data=data, headers=headers)
    with urllib.request.urlopen(req, timeout=15) as response:
        return json.load(response)


def show(record):
    print(json.dumps(record, indent=2, ensure_ascii=False))


def get_latest_record(service, endpoint, since):
    print('GET ' + endpoint)
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        records = request(service, endpoint, params={'since': since})
        if records:
            record = records[-1]
            show(record)
            return record
        time.sleep(1)
    raise RuntimeError('No new record from ' + endpoint)


def wait_for_hazard(source, record_id, occurred_at, warnings=None):
    print('GET /internal/hazards?source=' + source)
    # Hazard IDs are sorted by timestamp. Start at the displayed record's second.
    first_id = 'haz-' + get_time(occurred_at).strftime('%Y%m%dT%H%M%S')
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        after = first_id
        while True:
            page = request('aggregator', '/internal/hazards', params={
                'source': source, 'after': after, 'limit': 1000,
            })
            for hazard in page['items']:
                if hazard['source_ref_id'] != record_id:
                    continue
                # BMKG warnings can be stored in the next poll cycle.
                if warnings is not None:
                    saved_warnings = hazard['attributes'].get('tsunami_warnings', [])
                    if saved_warnings != warnings:
                        continue
                return hazard
            after = page['next_after']
            if not after:
                break
        time.sleep(1)
    raise RuntimeError('Aggregator has not stored ' + record_id)


def check_field(hazard, field, expected):
    actual = hazard.get(field)
    if actual != expected:
        raise RuntimeError(f'{field}: expected {expected!r}, got {actual!r}')


def check_bmkg(record, warnings, hazard):
    check_field(hazard, 'source', 'BMKG')
    check_field(hazard, 'hazard_type', 'SEISMIC')
    check_field(hazard, 'source_ref_id', record['event_id'])
    check_field(hazard, 'area_name', record['region_name'])
    check_field(hazard, 'latitude', record['epicenter_lat'])
    check_field(hazard, 'longitude', record['epicenter_lon'])

    magnitude = record['magnitude']
    if magnitude >= 6.5:
        severity = 'SIAGA'
    elif magnitude >= 5:
        severity = 'WASPADA'
    else:
        severity = 'NORMAL'

    if warnings:
        levels = ['Normal', 'Waspada', 'Siaga', 'Awas']
        highest = max(warnings, key=lambda warning: levels.index(warning['threat_level']))
        severity = highest['threat_level'].upper()
    check_field(hazard, 'severity', severity)

    canonical_fields = {'event_id', 'region_name', 'epicenter_lat', 'epicenter_lon', 'occurred_at'}
    attributes = {key: value for key, value in record.items() if key not in canonical_fields}
    if warnings:
        attributes['tsunami_warnings'] = warnings
    check_field(hazard, 'attributes', attributes)
    check_times(record['occurred_at'], hazard)
    show(hazard)
    print('[PASS] BMKG mapping sesuai.\n')


def check_pvmbg(record, hazard):
    name, latitude, longitude = VOLCANOES[record['volcano_id']]
    check_field(hazard, 'source', 'PVMBG')
    check_field(hazard, 'hazard_type', 'VOLCANIC')
    check_field(hazard, 'source_ref_id', record['report_id'])
    check_field(hazard, 'area_name', name)
    check_field(hazard, 'latitude', latitude)
    check_field(hazard, 'longitude', longitude)
    check_field(hazard, 'severity', record['alert_level'].upper())

    canonical_fields = {'report_id', 'reported_at', 'alert_level'}
    attributes = {key: value for key, value in record.items() if key not in canonical_fields}
    check_field(hazard, 'attributes', attributes)
    check_times(record['reported_at'], hazard)
    show(hazard)
    print('[PASS] PVMBG mapping sesuai.\n')


def check_times(source_time, hazard):
    if get_time(source_time) != get_time(hazard['occurred_at']):
        raise RuntimeError('occurred_at differs from the mock record')
    if not hazard.get('hazard_id') or not hazard.get('ingested_at'):
        raise RuntimeError('HazardEvent is incomplete')


def main():
    load_env()
    interval = int(ENV.get('EVENT_INTERVAL_SECONDS', '10'))
    since = (get_time(now()) - dt.timedelta(seconds=2 * interval)).isoformat()

    # 1. BMKG mock -> matching HazardEvent.
    print('1. BMKG')
    earthquake = get_latest_record('bmkg', '/seismic-events', since)
    print('GET /tsunami-warnings')
    all_warnings = request('bmkg', '/tsunami-warnings', params={'since': since})
    warnings = [warning for warning in all_warnings
                if warning['related_event_id'] == earthquake['event_id']]
    warnings.sort(key=lambda warning: warning['warning_id'])
    show(warnings)
    hazard = wait_for_hazard('BMKG', earthquake['event_id'], earthquake['occurred_at'], warnings)
    check_bmkg(earthquake, warnings, hazard)

    # 2. PVMBG initial schema -> matching HazardEvent.
    print('2. PVMBG sebelum perubahan')
    old_report = get_latest_record('pvmbg', '/volcanic-reports', since)
    old_hazard = wait_for_hazard('PVMBG', old_report['report_id'], old_report['reported_at'])
    check_pvmbg(old_report, old_hazard)

    # 3. Change the schema; services keep running.
    input('Enter → POST /admin/schema-version {"enabled": true} ')
    show(request('pvmbg', '/admin/schema-version', body={'enabled': True}))
    changed_at = now()

    # 4. A fresh PVMBG record must retain confidence_level in attributes.
    print('\n3. PVMBG setelah perubahan')
    new_report = get_latest_record('pvmbg', '/volcanic-reports', changed_at)
    if 'confidence_level' not in new_report:
        raise RuntimeError('New schema is missing confidence_level')
    new_hazard = wait_for_hazard('PVMBG', new_report['report_id'], new_report['reported_at'])
    check_pvmbg(new_report, new_hazard)
    print('[PASS] confidence_level → attributes.confidence_level')

    # 5. Existing records must remain readable with their original attributes.
    saved_old = wait_for_hazard('PVMBG', old_report['report_id'], old_report['reported_at'])
    if saved_old['attributes'] != old_hazard['attributes']:
        raise RuntimeError('Old record attributes changed')
    print('[PASS] Record awal tetap tersedia.')

    # 6. Restore the initial schema for the next demo.
    print('\nPOST /admin/schema-version {"enabled": false}')
    show(request('pvmbg', '/admin/schema-version', body={'enabled': False}))
    print('\n[PASS] Problem 1 selesai.')


if __name__ == '__main__':
    try:
        main()
    except (KeyboardInterrupt, EOFError):
        print('\nDemo dihentikan; skema tetap pada nilai terakhir.')
        sys.exit(130)
    except (OSError, RuntimeError, ValueError, KeyError) as error:
        print('\n[FAIL]', error)
        sys.exit(1)
