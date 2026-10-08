// M1 P2.2: sustained real HTTP traffic. Implementation assisted by Codex.
import http from 'k6/http';
import { sleep, fail } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const vus = Number(__ENV.LOAD_VUS || 50);
const seconds = Number(__ENV.LOAD_SECONDS || 65);
const limit = Number(__ENV.LOAD_LIMIT || 100);
if (vus < 50 || seconds < 60 || !Number.isInteger(vus) || !Number.isInteger(seconds)) {
  throw new Error('M1 requires >=50 VUs and >=60 sustained seconds');
}
const requests = new Counter('hazard_requests');
const successes = new Counter('hazard_successes');
const rejected = new Counter('hazard_429');
const failures = new Counter('hazard_failures');
const failureRate = new Rate('hazard_error_rate_excluding_429');
const allLatency = new Trend('hazard_latency_ms', true);
const successLatency = new Trend('hazard_success_latency_ms', true);
const refreshes = new Counter('auth_refresh_successes');
const authRejected = new Counter('auth_refresh_429');
const authErrors = new Rate('auth_refresh_error_rate');
const thresholds = {
  hazard_error_rate_excluding_429: ['rate<0.01'],
  hazard_successes: ['count>0'],
  auth_refresh_error_rate: ['rate==0'],
};
// Retain per-window submetrics as evidence that traffic lasted the entire duration.
for (let i = 0; i < Math.ceil(seconds / 5); i++) {
  thresholds[`hazard_requests{window:${i}}`] = ['count>=50'];
  thresholds[`hazard_successes{window:${i}}`] = ['count>0'];
  thresholds[`hazard_429{window:${i}}`] = ['count>=0'];
  thresholds[`hazard_failures{window:${i}}`] = ['count>=0'];
}
for (const status of [0, 200, 400, 401, 403, 429, 500, 503]) {
  thresholds[`hazard_requests{status:${status}}`] = ['count>=0'];
}
export const options = {
  scenarios: { sustained: { executor: 'constant-vus', vus, duration: `${seconds}s`, gracefulStop: '10s' } },
  setupTimeout: '60s',
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(50)', 'p(95)', 'p(99)'],
  thresholds,
  maxRedirects: 0,
  noConnectionReuse: false,
  noVUConnectionReuse: false,
  systemTags: ['status', 'method', 'name', 'scenario', 'expected_response'],
};
http.setResponseCallback(http.expectedStatuses(200, 429));
const root = __ENV.LOAD_CLIENT_URL;
const auth = __ENV.LOAD_AUTH_URL;
const identities = ['public', 'responder', 'analyst'];
const summaryFields = ['hazard_id', 'source', 'hazard_type', 'severity', 'area_name', 'occurred_at', 'ingested_at'];
const fullFields = [...summaryFields, 'source_ref_id', 'latitude', 'longitude', 'attributes'];
const runID = __ENV.LOAD_RUN_ID;
function parameters(name, token) {
  const headers = { 'Content-Type': 'application/json', 'X-Correlation-ID': `${runID}-vu-${__VU}` };
  if (token) headers.Authorization = `Bearer ${token}`;
  return { headers, timeout: '10s', tags: { name } };
}
export function setup() {
  const sessions = [];
  for (let i = 0; i < vus; i++) {
    const id = identities[i % 3];
    const secret = __ENV[`${id.toUpperCase()}_CLIENT_SECRET`];
    const r = http.post(`${auth}/auth/token`, JSON.stringify({ client_id: id, client_secret: secret }), parameters('auth_login'));
    if (r.status !== 200) fail(`Login setup failed with status ${r.status}`);
    const p = r.json();
    if (!p.access_token || !p.refresh_token || p.expires_in < 1) fail('Invalid token contract');
    sessions.push({ ...p, id, renewAt: Date.now() + p.expires_in * 1000 - 10000 });
  }
  // Validate seeded data before measurement rather than accepting empty 200 responses.
  for (const source of ['BMKG', 'PVMBG']) {
    const r = http.get(`${root}/hazards?source=${source}&limit=1`, parameters('preflight', sessions[0].access_token));
    if (r.status !== 200 || !r.json().items.length) fail(`No canonical data for ${source}`);
  }
  return sessions;
}
let session;
let started;
function validPage(r, source, identity) {
  if (r.headers['X-Correlation-Id'] !== `${runID}-vu-${__VU}`) return false;
  try {
    const p = r.json();
    const fields = identity === 'public' ? summaryFields : fullFields;
    return Array.isArray(p.items) && p.items.length > 0 &&
      p.items.every(h => h.source === source && Object.keys(h).length === fields.length && fields.every(f => f in h)) &&
      Array.isArray(p.sources) && p.sources.length === 1 && p.sources[0].source === source;
  } catch (_) { return false; }
}
export default function (data) {
  if (!session) { session = data[__VU - 1]; started = Date.now(); }
  if (Date.now() >= session.renewAt) {
    const r = http.post(`${auth}/auth/refresh`, JSON.stringify({ refresh_token: session.refresh_token }), parameters('auth_refresh'));
    authRejected.add(r.status === 429 ? 1 : 0);
    if (r.status === 429) { sleep(0.1); return; }
    if (r.status !== 200) { authErrors.add(true); fail(`Refresh failed with status ${r.status}`); }
    const p = r.json();
    if (!p.access_token || !p.refresh_token) { authErrors.add(true); fail('Invalid refresh response'); }
    authErrors.add(false);
    refreshes.add(1);
    session = { ...p, id: session.id, renewAt: Date.now() + p.expires_in * 1000 - 10000 };
  }
  const source = (__ITER + __VU) % 2 ? 'BMKG' : 'PVMBG';
  const window = String(Math.min(Math.floor((Date.now() - started) / 5000), Math.ceil(seconds / 5) - 1));
  const tags = { window, source, identity: session.id };
  const r = http.get(`${root}/hazards?source=${source}&limit=${limit}`, parameters('hazards', session.access_token));
  const controlled = r.status === 429 && Number(r.headers['Retry-After']) >= 1;
  const ok = r.status === 200 && validPage(r, source, session.id);
  requests.add(1, { ...tags, status: String(r.status) });
  successes.add(ok ? 1 : 0, tags);
  rejected.add(controlled ? 1 : 0, tags);
  failures.add(!ok && !controlled ? 1 : 0, tags);
  if (!controlled) failureRate.add(!ok);
  allLatency.add(r.timings.duration, tags);
  if (ok) successLatency.add(r.timings.duration, tags);
  // No think time: each VU maintains back-to-back requests for sustained pressure.
}
export function handleSummary(data) {
  return { [__ENV.LOAD_SUMMARY_PATH]: JSON.stringify({ metrics: data.metrics, state: data.state, options: data.options }, null, 2) + '\n' };
}
