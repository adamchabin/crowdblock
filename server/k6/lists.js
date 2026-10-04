// Read load: GET /api/v1/ips (full, conditional 304 and delta requests).
//   k6 run server/k6/lists.js
//   RATE=300 DURATION=2m k6 run server/k6/lists.js
import { setupUsers, teardownUsers, readList, num, runId } from './lib.js';

export const options = {
  scenarios: {
    lists: {
      executor: 'constant-arrival-rate',
      rate: num('RATE', 100), timeUnit: '1s', duration: __ENV.DURATION || '1m',
      preAllocatedVUs: num('VUS', 50), maxVUs: num('MAX_VUS', 200),
    },
  },
  setupTimeout: '5m', teardownTimeout: '5m',
  // Per scenario: setup() traffic (seeding) must not count.
  thresholds: { 'http_req_failed{scenario:lists}': ['rate<0.01'], 'http_req_duration{scenario:lists}': ['p(95)<500'],
    'http_reqs{scenario:lists}': ['count>=0'], checks: ['rate>0.99'] },
};

export const setup = () => setupUsers(num('USERS', 20), runId(), num('SEED', 50000));
export default (users) => readList(users);
export const teardown = teardownUsers;
