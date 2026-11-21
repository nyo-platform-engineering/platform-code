import http from 'k6/http';
import { Counter } from 'k6/metrics';

const successful = new Counter('successful_reads');
const unexpected = new Counter('unexpected_responses');
export const options = {
  stages: [{ duration: '30s', target: Number(__ENV.VUS || 48) },
    { duration: __ENV.HOLD || '180s', target: Number(__ENV.VUS || 48) },
    { duration: '15s', target: 0 }],
  thresholds: { successful_reads: ['count>0'], unexpected_responses: ['count==0'] },
};
let token = '';
let refreshAt = 0;
export default function () {
  if (Date.now() >= refreshAt) {
    const response = http.post(`${__ENV.AUTH_URL}/auth/token`, JSON.stringify({
      client_id: 'responder', client_secret: __ENV.RESPONDER_CLIENT_SECRET,
    }), { headers: { 'Content-Type': 'application/json' }, timeout: '5s' });
    if (response.status !== 200) { unexpected.add(1); return; }
    token = response.json('access_token');
    refreshAt = Date.now() + 45000;
  }
  const response = http.get(`${__ENV.CLIENT_URL}/hazards?limit=1000`, {
    headers: { Authorization: `Bearer ${token}` }, timeout: '5s',
  });
  if (response.status === 200) successful.add(1);
  else if (![429, 503].includes(response.status)) unexpected.add(1);
}
