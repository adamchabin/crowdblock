// Account lifecycle load (bcrypt-heavy, CPU bound): register, create key,
// list keys, report, revoke, delete account. Every iteration deletes its own
// account, so there is no setup/teardown.
//   k6 run server/k6/accounts.js
//   VUS=20 DURATION=2m k6 run server/k6/accounts.js
import { accountLifecycle, num, runId } from './lib.js';

export const options = {
  scenarios: {
    accounts: { executor: 'constant-vus', vus: num('VUS', 5), duration: __ENV.DURATION || '1m' },
  },
  thresholds: { http_req_failed: ['rate<0.01'], checks: ['rate>0.99'], 'http_req_duration{name:POST /register}': ['p(95)<1500'] },
};

export function setup() { return { runId: runId() }; }
export default (data) => accountLifecycle(data.runId);
