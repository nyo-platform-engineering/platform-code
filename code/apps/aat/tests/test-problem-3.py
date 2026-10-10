#!/usr/bin/env python3
"""Verify Problem 3: credential isolation, Media scope and natural token expiry.
Run after starting the base + client Compose stack. Default expiry takes 60s.
"""
from pathlib import Path
import re
import subprocess
import time

from test_helpers import IntegrationTest, RAW, ROOT, SECRET_KEYS, SUMMARY, dotenv, require, run_test


class Problem3Test(IntegrationTest):
    def secret_audit(self):
        values = [self.env[k] for k in SECRET_KEYS]
        require(len(set(values)) == len(values), 'Credential domains must use distinct values')
        require(all(len(v) >= 16 and not v.startswith('example-') for v in values), 'Replace example/short credentials with generated secrets')
        example = dotenv(ROOT / '.env.example')
        require(all(example.get(k) for k in SECRET_KEYS), '.env.example missing credential variables/examples')
        tracked = self.cmd(['git', 'ls-files', '-z']).split('\0')
        require('.env.example' in tracked, '.env.example must be tracked')
        require(not any(Path(p).name == '.env' or (Path(p).name.startswith('.env.') and Path(p).name != '.env.example') for p in tracked if p),
                'An environment credential file is tracked')
        ignored = subprocess.run(['git', 'check-ignore', '-q', str(self.args.env_file)], cwd=ROOT,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode
        require(ignored == 0, 'Selected env file must be ignored by Git')
        paths = self.cmd(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z']).split('\0')
        for name in paths:
            path = ROOT / name
            if not name or not path.is_file():
                continue
            content = path.read_bytes()
            require(not any(v.encode() in content for v in values), 'Actual configured credential found in app file: ' + name)
        literal = re.compile(r'(?:Token|Secret|APIKey|Password)\s*[:=]+\s*"([^"\n]+)"')
        for name in paths:
            path = ROOT / name
            if not name or path.suffix != '.go' or path.name.endswith('_test.go') or not path.is_file():
                continue
            require(not any(match.group(1) not in ('Bearer', 'Bearer ') for match in literal.finditer(path.read_text())),
                    'Possible hardcoded credential; review ' + str(path.relative_to(ROOT)))
        # Scan reachable AAT history for the current credentials.
        history = self.cmd(['git', 'log', '--all', '-p', '--format=', '--', '.'])
        require(not any(v in history for v in values), 'Current configured credential found in reachable Git history')
        historical_paths = self.cmd(['git', 'log', '--all', '--format=', '--name-only', '--', '.']).splitlines()
        require(not any(Path(p).name == '.env' or (Path(p).name.startswith('.env.') and Path(p).name != '.env.example') for p in historical_paths),
                'Credential environment file found in reachable Git history')
        self.passed('credential_config_and_repository_audit', {'scope': 'AAT files and reachable AAT Git history',
            'checks': ['distinct credentials', 'tracked .env.example', 'ignored env file',
                       'current credential values absent in files/history', 'production Go credential literal heuristic'],
            'limitation': 'Not a comprehensive secret scanner; historical secrets different from current values need separate review.'})

    def run(self):
        self.secret_audit()
        self.preflight()
        self.call('bmkg', '/seismic-events', headers={'X-BMKG-Key': self.env['BMKG_API_KEY']})
        self.call('pvmbg', '/volcanic-reports', token=self.env['PVMBG_TOKEN'])
        self.call('bmkg', '/seismic-events', headers={'X-BMKG-Key': self.env['PVMBG_TOKEN']}, expected=(401, 403))
        self.call('pvmbg', '/volcanic-reports', token=self.env['BMKG_API_KEY'], expected=(401, 403))
        self.passed('valid_upstream_credentials_rejected_cross_domain')
        public = self.login('public')
        for source in ('BMKG', 'PVMBG'):
            page = self.call('client', f'/hazards?source={source}&limit=100', token=public['access_token'])
            require(page.get('items') and all(set(h) == SUMMARY for h in page['items']), 'Media field allowlist violation: ' + source)
        for field in sorted(RAW) + ['severity,attributes']:
            self.call('client', '/hazards?fields=' + field, token=public['access_token'], expected=(401, 403))
        self.passed('media_summary_only_and_explicit_raw_fields_denied', {'allowed_fields': sorted(SUMMARY), 'denied_fields': sorted(RAW)})
        for service, path, headers in (('bmkg', '/seismic-events', {'X-BMKG-Key': public['access_token']}),
                                       ('pvmbg', '/volcanic-reports', {}), ('aggregator', '/internal/hazards', {})):
            self.call(service, path, token=public['access_token'], headers=headers, expected=(401, 403))
        self.passed('media_token_cannot_access_upstream_or_internal_store_api')

        responder = self.login('responder')
        start = time.monotonic()
        ttl = responder['expires_in']
        require(ttl == int(self.env.get('ACCESS_TTL_SECONDS', '60')), 'Runtime access TTL differs from configured TTL')
        require(int(self.env.get('REFRESH_TTL_SECONDS', '3600')) > ttl + 5, 'Refresh TTL must exceed access TTL for natural expiry demo')
        reads = 0
        print(f'[INFO] active Tim Lapangan session: waiting natural expiry, TTL={ttl}s', flush=True)
        while True:
            try:
                page = self.call('client', '/hazards?source=BMKG&limit=1', token=responder['access_token'])
            except RuntimeError as error:
                if 'HTTP 401' not in str(error):
                    raise
                break
            require(page.get('items') and all(set(h) == SUMMARY | RAW for h in page['items']), 'Tim Lapangan full fields absent')
            reads += 1
            elapsed = time.monotonic() - start
            require(elapsed < ttl + self.args.timeout, 'Access token failed to expire naturally')
            print(f'[INFO] active session {elapsed:.0f}/{ttl}s, successful reads={reads}', flush=True)
            time.sleep(min(5, max(.1, ttl + 1 - elapsed)))
        elapsed = time.monotonic() - start
        require(reads > 0 and elapsed >= max(0, ttl - 2), 'Token rejected before natural TTL expiry')
        fresh = self.refresh(responder)
        require(fresh.get('access_token') != responder['access_token'] and fresh.get('refresh_token') != responder['refresh_token'], 'Refresh did not rotate both tokens')
        page = self.call('client', '/hazards?source=BMKG&limit=1', token=fresh['access_token'])
        require(page.get('items') and all(set(h) == SUMMARY | RAW for h in page['items']), 'Refreshed Tim Lapangan session lost scope')
        self.call('client', '/hazards', token=responder['access_token'], expected=(401, 403))
        self.passed('natural_expiry_refresh_without_login_and_old_access_denied', {'ttl_seconds': ttl,
            'elapsed_seconds': elapsed, 'active_session_reads': reads, 'logins': 1})
        self.call('auth', '/auth/refresh', {'refresh_token': responder['refresh_token']}, expected=(401, 403))
        self.call('client', '/hazards', token=fresh['access_token'], expected=(401, 403))
        self.passed('refresh_replay_rejected_and_session_revoked')


if __name__ == '__main__':
    run_test('3', Problem3Test, __doc__)
