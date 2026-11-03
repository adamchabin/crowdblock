// Shared helpers of the k6 scenarios. Every scenario creates its own test
// users (e-mail k6-*@example.test) and deletes them again through
// DELETE /api/v1/account, which cascades to their keys and reports.
import http from 'k6/http';
import encoding from 'k6/encoding';
import { check, fail, sleep } from 'k6';

export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
export const PASSWORD = 'k6-load-test-pw';
const JSON_HEADERS = { 'Content-Type': 'application/json' };

export const basic = (email) => ({
  Authorization: 'Basic ' + encoding.b64encode(`${email}:${PASSWORD}`),
});

// ---------- users: created in setup(), removed in teardown() ----------

// Creates n users with one API key each (bcrypt makes this slow, so the
// requests go out in parallel) and, if seed > 0, `seed` distinct reported
// addresses spread over them. Rolls back everything if anything fails.
export function setupUsers(n, runId, seed = 0) {
  const users = Array.from({ length: n }, (_, i) => ({ email: `k6-${runId}-${i}@example.test` }));
  try {
    const reg = http.batch(users.map((u) => ['POST', `${BASE_URL}/api/v1/register`,
      JSON.stringify({ email: u.email, password: PASSWORD }), { headers: JSON_HEADERS, timeout: '120s' }]));
    reg.forEach((r, i) => { if (r.status !== 201) fail(`register ${users[i].email}: ${r.status} ${r.body}`); });

    const keys = http.batch(users.map((u) => ['POST', `${BASE_URL}/api/v1/api-keys`,
      JSON.stringify({ name: 'k6' }), { headers: { ...JSON_HEADERS, ...basic(u.email) }, timeout: '120s' }]));
    keys.forEach((r, i) => {
      if (r.status !== 201) fail(`create key ${users[i].email}: ${r.status} ${r.body}`);
      users[i].apiKey = r.json('api_key');
    });
    if (seed > 0) seedReports(users, seed);
  } catch (e) {
    teardownUsers(users); // the exception is rethrown after the cleanup
    throw e;
  }
  return users;
}

// Reports `count` distinct addresses (user i % n reports address i / n of
// its slice), then waits until the precomputed list contains them all.
function seedReports(users, count) {
  const n = users.length;
  const slice = Math.floor(131072 / n);
  if (Math.ceil(count / n) > slice) fail(`SEED=${count} too large for ${n} users`);
  const ip = (i) => { const off = (i % n) * slice + Math.floor(i / n); return `198.${18 + (off >> 16)}.${(off >> 8) & 255}.${off & 255}`; };

  for (let from = 0; from < count; from += 500) {
    const batch = [];
    for (let i = from; i < Math.min(from + 500, count); i++) {
      batch.push(['POST', `${BASE_URL}/api/v1/reports`, JSON.stringify({ ip: ip(i), source: 'k6' }),
        { headers: { ...JSON_HEADERS, 'X-API-Key': users[i % n].apiKey }, responseType: 'none' }]);
    }
    http.batch(batch).forEach((r) => { if (r.status !== 201) fail(`seed report: ${r.status}`); });
  }

  // One entry per line + 2 bracket lines; lists are refreshed every LIST_REFRESH (1 min by default).
  const deadline = Date.now() + num('SEED_WAIT', 180) * 1000;
  for (;;) {
    const r = http.get(`${BASE_URL}/api/v1/ips?min_reporters=1&minutes=60`,
      { headers: { 'X-API-Key': users[0].apiKey }, timeout: '120s' });
    if (r.status === 200 && (r.body.match(/\n/g) || []).length - 2 >= count) break;
    if (Date.now() > deadline) fail(`list still lacks the ${count} seeded addresses after SEED_WAIT`);
    sleep(2);
  }
  console.log(`seeded ${count} addresses`);
}

// Deletes the users (their keys and reports go with them). 401 = never created.
export function teardownUsers(users) {
  const res = http.batch(users.map((u) => ['DELETE', `${BASE_URL}/api/v1/account`, null,
    { headers: basic(u.email), timeout: '120s' }]));
  res.forEach((r, i) => {
    if (r.status !== 204 && r.status !== 401) console.error(`cleanup ${users[i].email}: ${r.status} ${r.body}`);
  });
  console.log(`cleanup: ${res.filter((r) => r.status === 204).length}/${users.length} test accounts deleted`);
}

// ---------- scenario bodies ----------

// Addresses come from 198.18.0.0/15 (RFC 2544 benchmark range), split into
// one slice per user: an address is reported by ONE user only, so the
// blacklist threshold is never reached and nothing outlives the accounts
// (the blacklist table has no delete endpoint). Repeats just add rows.
function reportedIP(idx, n) {
  const slice = Math.floor(131072 / n);
  const off = idx * slice + Math.floor(Math.random() * slice);
  return `198.${18 + (off >> 16)}.${(off >> 8) & 255}.${off & 255}`;
}

// POST /api/v1/reports as the user belonging to this VU.
export function reportIP(users) {
  const idx = __VU % users.length;
  const r = http.post(`${BASE_URL}/api/v1/reports`,
    JSON.stringify({ ip: reportedIP(idx, users.length), source: 'k6' }),
    { headers: { ...JSON_HEADERS, 'X-API-Key': users[idx].apiKey }, tags: { name: 'POST /reports' } });
  check(r, { 'report 201': (x) => x.status === 201 });
}

// GET /api/v1/ips like a router: the full list first, then full, conditional
// (304) and delta requests with the ETag of the last answer for that tier
// (kept per VU).
// (min_reporters > 1 would need addresses reported by several users, which
// would leave rows in the blacklist table: those tiers are not used.)
const TIERS = [[1, 60], [1, 360], [1, 1440], [1, 10080]];
const MODES = ['full', 'conditional', 'delta'];
const etags = {};
export function readList(users) {
  const [minReporters, minutes] = TIERS[Math.floor(Math.random() * TIERS.length)];
  const tier = `${minReporters}/${minutes}`;
  const mode = etags[tier] ? MODES[Math.floor(Math.random() * MODES.length)] : 'full';

  const headers = { 'X-API-Key': users[__VU % users.length].apiKey };
  if (mode !== 'full') headers['If-None-Match'] = etags[tier];
  const url = `${BASE_URL}/api/v1/ips?min_reporters=${minReporters}&minutes=${minutes}` + (mode === 'delta' ? '&delta=1' : '');

  const r = http.get(url, { headers, responseType: 'none', tags: { name: `GET /ips (${mode})` } });
  check(r, { 'list 200/304': (x) => x.status === 200 || x.status === 304 });
  if (r.status === 200 && r.headers['Etag']) etags[tier] = r.headers['Etag'];
}

// The whole account life: register, create key, list, report, revoke,
// delete. The account is deleted even when a step fails.
export function accountLifecycle(runId) {
  const email = `k6-${runId}-acct-${__VU}-${__ITER}@example.test`;
  const auth = { ...JSON_HEADERS, ...basic(email) };
  const reg = http.post(`${BASE_URL}/api/v1/register`,
    JSON.stringify({ email, password: PASSWORD }), { headers: JSON_HEADERS, tags: { name: 'POST /register' } });
  if (!check(reg, { 'register 201': (x) => x.status === 201 })) return;

  try {
    const created = http.post(`${BASE_URL}/api/v1/api-keys`, '{"name":"k6"}', { headers: auth, tags: { name: 'POST /api-keys' } });
    if (!check(created, { 'create key 201': (x) => x.status === 201 })) return;
    const { id, api_key: apiKey } = created.json();

    check(http.get(`${BASE_URL}/api/v1/api-keys`, { headers: auth, tags: { name: 'GET /api-keys' } }),
      { 'list keys 200': (x) => x.status === 200 });
    check(http.post(`${BASE_URL}/api/v1/reports`, JSON.stringify({ ip: reportedIP(0, 1), source: 'k6' }),
      { headers: { ...JSON_HEADERS, 'X-API-Key': apiKey }, tags: { name: 'POST /reports' } }),
      { 'report 201': (x) => x.status === 201 });
    check(http.del(`${BASE_URL}/api/v1/api-keys/${id}`, null, { headers: auth, tags: { name: 'DELETE /api-keys/{id}' } }),
      { 'revoke 204': (x) => x.status === 204 });
  } finally {
    check(http.del(`${BASE_URL}/api/v1/account`, null, { headers: auth, tags: { name: 'DELETE /account' } }),
      { 'delete account 204': (x) => x.status === 204 });
  }
}

export const num = (name, def) => parseInt(__ENV[name] || def, 10);
export const runId = () => __ENV.RUN_ID || Date.now().toString(36);
