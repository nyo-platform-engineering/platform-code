#!/usr/bin/env python3
"""Low-resource demo traffic: one in-flight request, standard library only."""
import argparse
import json
import math
import random
import signal
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


def make_request(rng):
    action = rng.choices(['success', 'slow', 'error'], weights=[70, 20, 10])[0]
    params = {
        'source': 'bot',
        'customer_id': f'customer-{rng.randint(1, 5):02d}',
        'region': rng.choice(['ap-southeast-1', 'eu-west-1', 'us-east-1']),
        'product': rng.choice(['book', 'keyboard', 'coffee', 'headphones']),
        'quantity': rng.randint(1, 10),
        'delay_ms': rng.randint(250, 1500) if action == 'slow' else rng.randint(0, 100),
    }
    return action, params


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', default='http://127.0.0.1:8081', help='Go demo base URL')
    parser.add_argument('--interval', type=float, default=1, help='Average pause between requests in seconds (jitter ±50%%)')
    parser.add_argument('--count', type=int, default=0, help='Stop after this many requests; 0 runs until Ctrl-C')
    parser.add_argument('--seed', type=int, help='Repeatable random request sequence')
    args = parser.parse_args()
    if not math.isfinite(args.interval) or args.interval < .05 or args.count < 0:
        parser.error('interval must be finite and >= 0.05 seconds; count must be >= 0')
    target = urllib.parse.urlsplit(args.url)
    if target.scheme not in ('http', 'https') or not target.netloc or target.query or target.fragment or target.username:
        parser.error('url must be an HTTP(S) base URL without credentials, query, or fragment')
    stop = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stop.set())
    rng = random.Random(args.seed)
    # Never send demo traffic through a configured external proxy or follow redirects.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    counts = dict(sent=0, success=0, expected_errors=0, unexpected=0)
    print(f'Traffic bot → {args.url} · 70% success / 20% slow / 10% error · Ctrl-C to stop', flush=True)
    while not stop.is_set() and (args.count == 0 or counts['sent'] < args.count):
        action, params = make_request(rng)
        url = args.url.rstrip('/') + '/demo/' + action + '?' + urllib.parse.urlencode(params)
        started = time.monotonic()
        counts['sent'] += 1
        try:
            try:
                response = client.open(urllib.request.Request(url, method='POST'), timeout=5)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                code = response.code
                body = json.loads(response.read(65536))
            expected = 500 if action == 'error' else 200
            valid = code == expected and body.get('action') == action
            counts['expected_errors' if valid and code == 500 else 'success' if valid else 'unexpected'] += 1
            print(json.dumps(dict(request=counts['sent'], action=action, status=code, expected=valid,
                                  duration_ms=round((time.monotonic()-started)*1000), trace_id=body.get('trace_id'), **params)), flush=True)
        except (OSError, urllib.error.URLError, ValueError, AttributeError) as error:
            counts['unexpected'] += 1
            print(json.dumps(dict(request=counts['sent'], error=str(error))), flush=True)
        if args.count and counts['sent'] >= args.count:
            break
        stop.wait(args.interval * rng.uniform(.5, 1.5))
    print('Summary: ' + json.dumps(counts), flush=True)
    return 1 if counts['unexpected'] else 0


if __name__ == '__main__':
    sys.exit(main())
