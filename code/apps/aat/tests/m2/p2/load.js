// M2 P2: sustained traffic through Traefik to the Aggregator Service.
import http from 'k6/http';
import exec from 'k6/execution';
import { sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const vus = Number(__ENV.P2_VUS);
const seconds = Number(__ENV.P2_SECONDS);
const warmup = Number(__ENV.P2_WARMUP);
const targetRPS = Number(__ENV.P2_TARGET_RPS || 0);
if (!Number.isFinite(targetRPS) || targetRPS < 0 || (targetRPS > 0 && seconds < vus / targetRPS + 10)) {
  throw new Error('Paced window must exercise every VU, with 10s margin');
}
if (![vus, seconds, warmup].every(Number.isInteger) || vus < 1 || seconds < 15 || warmup < 1) {
  throw new Error('Invalid P2 load configuration');
}
const requests = new Counter('p2_requests');
const successes = new Counter('p2_successes');
const exercised = new Counter('p2_vus_exercised');
const rejected = new Counter('p2_controlled_rejections');
const failures = new Counter('p2_failures');
const errorRate = new Rate('p2_error_rate_excluding_controlled');
const latency = new Trend('p2_latency_ms', true);
const active = new Trend('p2_active_vus');
const statuses = new Counter('p2_http_status');
const cacheResults = new Counter('p2_cache_results');
const thresholds = { p2_error_rate_excluding_controlled: ['rate<0.01'], p2_successes: ['count>0'] };
if (targetRPS > 0) thresholds.p2_vus_exercised = [`count>=${vus}`];
for (const status of [0, 200, 429, 500, 502, 503, 504]) {
  thresholds[`p2_http_status{status:${status}}`] = ['count>=0'];
}
for (const result of ['hit', 'miss', 'uncached', 'limited']) {
  thresholds[`p2_cache_results{result:${result}}`] = ['count>=0'];
}
for (let i = 0; i < Math.ceil(seconds / 5); i++) {
  thresholds[`p2_requests{window:${i}}`] = ['count>0'];
  thresholds[`p2_successes{window:${i}}`] = ['count>0'];
  thresholds[`p2_active_vus{window:${i}}`] = [`min>=${vus}`];
}
export const options = {
  scenarios: { sustained: { executor: 'constant-vus', vus, duration: `${seconds + warmup}s`, gracefulStop: '10s' } },
  // Paced VUs must not each retain an idle gateway connection between slots.
  noConnectionReuse: true,
  maxRedirects: 0,
  summaryTrendStats: ['avg', 'min', 'max', 'p(50)', 'p(95)', 'p(99)'],
  thresholds,
};
let announced = false;
let measuredOnce = false;
let pacedIteration = 0;
export default function () {
  const elapsedNow = () => (Date.now() - exec.scenario.startTime) / 1000 - warmup;
  let elapsed = elapsedNow();
  if (targetRPS > 0) {
    // VU 1 warms the query; other VUs wait for evenly staggered measured slots.
    // All 10,000 remain allocated, and every VU must issue a measured request.
    const offset = (exec.vu.idInTest - 1) / targetRPS;
    if (elapsed < 0 && __VU !== 1) {
      sleep(-elapsed + offset);
      elapsed = elapsedNow();
    }
    if (elapsed >= 0) {
      if (!announced && __VU === 1) {
        console.log('P2_MEASUREMENT_START');
        announced = true;
      }
      const slot = offset + pacedIteration * vus / targetRPS;
      pacedIteration++;
      if (slot >= seconds) {
        sleep(Math.max(0.01, seconds - elapsed + 0.05));
        return;
      }
      if (slot > elapsed) sleep(slot - elapsed);
      elapsed = elapsedNow();
    }
  }
  const measuring = elapsed >= 0 && elapsed < seconds;
  if (measuring && !announced && __VU === 1) {
    console.log('P2_MEASUREMENT_START');
    announced = true;
  }
  const window = Math.min(Math.floor(elapsed / 5), Math.ceil(seconds / 5) - 1);
  const activeAtStart = exec.instance.vusActive;
  const response = http.get(`${__ENV.P2_URL}/internal/hazards?order=recent&limit=${__ENV.P2_LIMIT}`, {
    headers: { Authorization: `Bearer ${__ENV.P2_TOKEN}`, Host: __ENV.P2_HOST,
      'Accept-Encoding': 'gzip',
      'X-Correlation-ID': `${__ENV.P2_RUN_ID}-${__VU}-${__ITER}` },
    timeout: '8s', tags: { name: 'aggregator-hazards' },
  });
  const retryAfter = Number(response.headers['Retry-After']);
  const controlled = response.status === 429 && Number.isFinite(retryAfter) && retryAfter >= 1;
  if (!measuring) {
    if (controlled) sleep(Math.min(retryAfter, 5));
    else if (response.status !== 200) sleep(1);
    else if (targetRPS > 0) sleep(0.1);
    return;
  }
  let valid = false;
  try {
    const page = response.json();
    valid = response.status === 200 && Array.isArray(page.items) && page.items.length > 0 &&
      page.partial === false && Array.isArray(page.unavailable_units) && page.unavailable_units.length === 0 &&
      page.items.every(item => item.hazard_id && item.source && item.source_ref_id);
  } catch (_) { /* Invalid responses count as failures. */ }
  const tags = { window: String(window) };
  requests.add(1, tags);
  if (!measuredOnce) { exercised.add(1); measuredOnce = true; }
  statuses.add(1, {status: String(response.status)});
  cacheResults.add(1, {result: controlled ? 'limited' : (response.headers['X-Hazard-Cache'] || 'uncached')});
  successes.add(valid ? 1 : 0, tags);
  failures.add(!valid && !controlled ? 1 : 0);
  rejected.add(controlled ? 1 : 0);
  if (!controlled) errorRate.add(!valid);
  latency.add(response.timings.duration);
  active.add(activeAtStart, tags);
  if (controlled) sleep(Math.min(retryAfter, 5));
  else if (!valid) sleep(1); // Record failures, but never spin on fast refusals.
}
export function handleSummary(data) {
  return { [__ENV.P2_SUMMARY]: JSON.stringify(data, null, 2) };
}
