import http from 'k6/http';
import { check } from 'k6';

// Methodology: VUS virtual users for DURATION against BASE_URL (default 10/60s).
// Measures HTTP create/assign response times (first-offer row commit is ~same path;
// full offer-commit latency is asserted by worker integration tests). Record
// machine, compose versions, warm-up, sample size with any published numbers.
export const options = {
  vus: Number(__ENV.VUS || 10),
  duration: __ENV.DURATION || '60s',
  thresholds: {
    'http_req_duration{op:create}': ['p(95)<100'],
    'http_req_duration{op:assign}': ['p(95)<500'],
    'http_req_failed': ['rate<0.01'],
  },
};

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';

export default function () {
  const key = `load-${__VU}-${__ITER}-${Date.now()}`;
  let r = http.post(`${BASE}/v1/orders`, JSON.stringify({ restaurant_picker: 'random', priority: 'normal' }),
    { headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key }, tags: { op: 'create' } });
  check(r, { 'create 201': (x) => x.status === 201 });
  if (r.status !== 201) return;
  const oid = r.json().order.id;
  http.post(`${BASE}/v1/orders/${oid}/prepare`, null, { tags: { op: 'transition' } });
  http.post(`${BASE}/v1/orders/${oid}/ready`, null, { tags: { op: 'transition' } });
  let a = http.post(`${BASE}/v1/orders/${oid}/assign`, null,
    { headers: { 'Idempotency-Key': `assign-${oid}` }, tags: { op: 'assign' } });
  check(a, { 'assign 201': (x) => x.status === 201 });
}
