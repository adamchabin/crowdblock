// 1000 different clients: each one reports attacking addresses with its own
// API key (created in setup(), deleted in teardown()).
//   k6 run server/k6/thousand-clients.js
//   USERS=1000 VUS=1000 DURATION=2m k6 run server/k6/thousand-clients.js
import { setupUsers, teardownUsers, reportIP, num, runId } from './lib.js';

export const options = {
  scenarios: {
    clients: {
      executor: 'constant-vus',
      vus: num('VUS', 1000),
      duration: __ENV.DURATION || '2m',
    },
  },
  setupTimeout: '10m', teardownTimeout: '5m',
  thresholds: {
    'http_req_failed{scenario:clients}': ['rate<0.01'],
    checks: ['rate>0.99'],
    'http_req_duration{scenario:clients}': ['p(95)<2000'],
  },
};

export const setup = () => setupUsers(num('USERS', 1000), runId(), 0);
export default (users) => reportIP(users);
export const teardown = teardownUsers;
