// Realistic mix at the same time: many routers reading, some reporters
// writing, a few accounts coming and going.
//   k6 run server/k6/mixed.js
//   DURATION=5m READ_RATE=200 WRITE_RATE=100 k6 run server/k6/mixed.js
import { setupUsers, teardownUsers, reportIP, readList, accountLifecycle, num, runId } from './lib.js';

const duration = __ENV.DURATION || '2m';
const pool = (rate) => ({ executor: 'constant-arrival-rate', rate, timeUnit: '1s', duration,
  preAllocatedVUs: 50, maxVUs: 200 });

export const options = {
  scenarios: {
    read: { ...pool(num('READ_RATE', 100)), exec: 'read' },
    write: { ...pool(num('WRITE_RATE', 50)), exec: 'write' },
    accounts: { executor: 'constant-arrival-rate', rate: num('ACCOUNT_RATE', 1), timeUnit: '1s', duration,
      preAllocatedVUs: 5, maxVUs: 20, exec: 'account' },
  },
  setupTimeout: '5m', teardownTimeout: '5m',
  thresholds: { 'http_req_failed{scenario:read}': ['rate<0.01'], 'http_req_failed{scenario:write}': ['rate<0.01'],
    'http_reqs{scenario:read}': ['count>=0'], 'http_reqs{scenario:write}': ['count>=0'], checks: ['rate>0.99'],
    'http_req_duration{scenario:read}': ['p(95)<500'], 'http_req_duration{scenario:write}': ['p(95)<300'] },
};

export function setup() {
  const id = runId();
  return { users: setupUsers(num('USERS', 20), id, num('SEED', 50000)), runId: id };
}
export const read = (d) => readList(d.users);
export const write = (d) => reportIP(d.users);
export const account = (d) => accountLifecycle(d.runId);
export const teardown = (d) => teardownUsers(d.users);
