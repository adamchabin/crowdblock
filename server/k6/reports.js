// Write load: POST /api/v1/reports at a constant rate.
//   k6 run server/k6/reports.js
//   RATE=500 DURATION=2m USERS=20 BASE_URL=http://host:8080 k6 run server/k6/reports.js
import { setupUsers, teardownUsers, reportIP, num, runId } from './lib.js';

export const options = {
  scenarios: {
    reports: {
      executor: 'constant-arrival-rate',
      rate: num('RATE', 200), timeUnit: '1s', duration: __ENV.DURATION || '1m',
      preAllocatedVUs: num('VUS', 50), maxVUs: num('MAX_VUS', 200),
    },
  },
  setupTimeout: '5m', teardownTimeout: '5m',
  // Per scenario: setup() traffic (seeding) must not count.
  thresholds: { 'http_req_failed{scenario:reports}': ['rate<0.01'], 'http_req_duration{scenario:reports}': ['p(95)<300'],
    'http_reqs{scenario:reports}': ['count>=0'], checks: ['rate>0.99'] },
};

export const setup = () => setupUsers(num('USERS', 20), runId(), num('SEED', 0));
export default (users) => reportIP(users);
export const teardown = teardownUsers;
