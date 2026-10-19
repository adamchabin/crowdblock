// 10 000 klientów pobierających listę IP co minutę (~167 req/s).
//   k6 run server/k6/ten-thousand-readers.js
//   USERS=10000 DURATION=1m k6 run server/k6/ten-thousand-readers.js
import { setupUsers, teardownUsers, readList, num, runId } from './lib.js';

const ratePerSecond = num('RATE', Math.round(10000 / 60)); // domyślnie ~10000 na minutę

export const options = {
  scenarios: {
    readers: {
      executor: 'constant-arrival-rate',
      rate: ratePerSecond,
      timeUnit: '1s',
      duration: __ENV.DURATION || '1m',
      preAllocatedVUs: num('VUS', 1000),
      maxVUs: num('MAX_VUS', 10000),
    },
  },
  setupTimeout: '15m', teardownTimeout: '5m',
  thresholds: {
    'http_req_failed{scenario:readers}': ['rate<0.01'],
    checks: ['rate>0.99'],
    'http_req_duration{scenario:readers}': ['p(95)<1000'],
  },
};

export const setup = () => setupUsers(num('USERS', 10000), runId(), 0);
export default (users) => readList(users);
export const teardown = teardownUsers;
