// Functional tests for crowdblock API — verifies response bodies, not just
// HTTP status codes. Run with:
//
//   k6 run server/k6/functional.js --env BASE_URL=http://localhost:8080
//   DURATION=1m ./server/run-tests.sh --functional
import http from 'k6/http';
import encoding from 'k6/encoding';
import { check, fail, sleep } from 'k6';

export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
export const PASSWORD = 'k6-functional-pw';
const JSON_HEADERS = { 'Content-Type': 'application/json' };

export const basic = (email) => ({
  Authorization: 'Basic ' + encoding.b64encode(`${email}:${PASSWORD}`),
});

export const options = {
  scenarios: {
    functional: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
    },
  },
  thresholds: { checks: ['rate==1.00'] },
  setupTimeout: '2m',
  teardownTimeout: '1m',
};

function assert(cond, msg) {
  if (!cond) fail(msg);
}

export function setup() {
  const email = `k6-func-${Date.now()}@example.test`;

  // 1. Register
  const reg = http.post(`${BASE_URL}/api/v1/register`,
    JSON.stringify({ email, password: PASSWORD }), { headers: JSON_HEADERS });
  check(reg, { 'register 201': (r) => r.status === 201 });
  const regBody = reg.json();
  assert(typeof regBody.user_id === 'string' && regBody.user_id.length > 0,
    'register response missing user_id');
  console.log(`registered ${email} (user_id=${regBody.user_id})`);

  // 2. Create API key
  const created = http.post(`${BASE_URL}/api/v1/api-keys`, '{"name":"func-test"}',
    { headers: { ...JSON_HEADERS, ...basic(email) } });
  check(created, { 'create key 201': (r) => r.status === 201 });
  const keyBody = created.json();
  assert(typeof keyBody.id === 'string' && keyBody.id.length > 0, 'missing key id');
  assert(typeof keyBody.api_key === 'string' && keyBody.api_key.length > 0, 'missing api_key');
  const apiKey = keyBody.api_key;
  console.log(`created api key ${keyBody.id}`);

  // 3. Report an IP
  const testIP = '203.0.113.42';
  const rep = http.post(`${BASE_URL}/api/v1/reports`,
    JSON.stringify({ ip: testIP, source: 'func-test' }),
    { headers: { ...JSON_HEADERS, 'X-API-Key': apiKey } });
  check(rep, { 'report 201': (r) => r.status === 201 });
  const repBody = rep.json();
  assert(repBody.accepted === true, 'report not accepted');
  console.log(`reported ${testIP}`);

  // 4. List IPs — should contain our address (min_reporters=1)
  const list = http.get(`${BASE_URL}/api/v1/ips?min_reporters=1&minutes=60`,
    { headers: { 'X-API-Key': apiKey } });
  check(list, { 'list 200': (r) => r.status === 200 });
  const ips = list.json();
  assert(Array.isArray(ips), 'list is not an array');
  const found = ips.some((e) => e.ip === testIP);
  assert(found, `reported IP ${testIP} not in list`);
  console.log(`found ${testIP} in list (${ips.length} entries)`);

  // 5. Health endpoint
  const health = http.get(`${BASE_URL}/api/v1/health`);
  check(health, { 'health 200': (r) => r.status === 200 });
  const hb = health.json();
  assert(hb.status === 'ok', 'health status not ok');
  assert(hb.postgres === 'up', 'postgres not up');
  assert(hb.redis === 'up', 'redis not up');
  console.log('health ok');

  // 6. GeoIP
  const geo = http.get(`${BASE_URL}/api/v1/geoip/8.8.8.8`);
  check(geo, { 'geoip 200': (r) => r.status === 200 });
  const gb = geo.json();
  assert(typeof gb.ip === 'string', 'missing ip in geoip');
  assert(typeof gb.country === 'string', 'missing country in geoip');
  console.log(`geoip 8.8.8.8 -> ${gb.country}`);

  // 7. Invalid API key -> 401
  const badKey = http.post(`${BASE_URL}/api/v1/reports`,
    JSON.stringify({ ip: '203.0.113.43', source: 'func-test' }),
    { headers: { ...JSON_HEADERS, 'X-API-Key': 'sfw_invalid' } });
  check(badKey, { 'invalid key 401': (r) => r.status === 401 });

  // 8. Missing API key -> 401
  const noKey = http.post(`${BASE_URL}/api/v1/reports`,
    JSON.stringify({ ip: '203.0.113.44', source: 'func-test' }),
    { headers: JSON_HEADERS });
  check(noKey, { 'no key 401': (r) => r.status === 401 });

  // 9. Delete account (cleanup)
  const del = http.del(`${BASE_URL}/api/v1/account`, null, { headers: basic(email) });
  check(del, { 'delete account 204': (r) => r.status === 204 });
  console.log(`deleted account ${email}`);

  return { email };
}

export default () => {
  // Single iteration — everything done in setup()
};

export function teardown(data) {
  // cleanup already done in setup()
  console.log(`teardown ${data.email}`);
}
