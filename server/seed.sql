-- Development seed data, applied after schema.sql only when SEED_DEV_DATA=1
-- (see EnsureSeed in migrate.go). Never enable it in production: it creates
-- users with a known password and API keys with known values.
--
-- Users (password for all: testtest):
--   dev                       - the user to log in / create API keys with
--   dev-reporter1..5          - extra reporters, so IPs can reach min_reporters
--
-- API keys (one per user, value = 'sfw_dev_' || email), e.g. for the router:
--   sfw_dev_dev
--
-- Reporter users (password testtest) for crowdblock-reporter, one per machine:
--   reporter1..3              - API keys sfw_reporter1 .. sfw_reporter3
-- Their reports are real test data and are NOT deleted by the seed.
--
-- Safe to run repeatedly: users and keys are created once, the sample reports
-- of the dev* users are replaced on every run so that they stay inside the
-- listing window.

INSERT INTO users (email, password_hash)
SELECT email, crypt('testtest', gen_salt('bf', 10))
FROM (VALUES ('dev'), ('dev-reporter1'), ('dev-reporter2'), ('dev-reporter3'),
             ('dev-reporter4'), ('dev-reporter5')) AS u(email)
ON CONFLICT (email) DO NOTHING;

-- Real keys are 'sfw_' + hex, so the 'sfw_dev_' prefix can't clash with them.
INSERT INTO api_keys (user_id, key_prefix, key_hash, name)
SELECT id, left('sfw_dev_' || email, 8),
       encode(digest('sfw_dev_' || email, 'sha256'), 'hex'), 'dev-seed'
FROM users
WHERE email = 'dev' OR email LIKE 'dev-reporter_'
ON CONFLICT (key_hash) DO NOTHING;

-- Keys for crowdblock-reporter: 'sfw_reporter1' .. 'sfw_reporter3'.
INSERT INTO users (email, password_hash)
SELECT 'reporter' || n, crypt('testtest', gen_salt('bf', 10))
FROM generate_series(1, 3) AS n
ON CONFLICT (email) DO NOTHING;

INSERT INTO api_keys (user_id, key_prefix, key_hash, name)
SELECT id, left('sfw_' || email, 8), encode(digest('sfw_' || email, 'sha256'), 'hex'), 'dev-seed-reporter'
FROM users
WHERE email IN ('reporter1', 'reporter2', 'reporter3')
ON CONFLICT (key_hash) DO NOTHING;

DELETE FROM ip_reports
WHERE api_key_id IN (SELECT id FROM api_keys WHERE key_prefix = 'sfw_dev_');

-- Each IP is reported by the first `reporters` seed users, `age` ago.
-- Documentation ranges (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24,
-- 2001:db8::/32) are used on purpose: the server lists them, but the OpenWrt
-- client never blocks them, so a router pointed at a dev server stays safe.
-- Sources rotate between 'auth', 'fail2ban' and none (reports from clients
-- that don't send it).
INSERT INTO ip_reports (ip, api_key_id, user_id, reported_at, source)
SELECT s.ip::inet, k.id, k.user_id, now() - s.age,
       (ARRAY['auth', 'fail2ban', NULL])[1 + k.n % 3]
FROM (VALUES
	('198.51.100.10',  6, interval '2 minutes'),
	('198.51.100.11',  5, interval '5 minutes'),
	('203.0.113.20',   5, interval '20 minutes'),
	('203.0.113.21',   3, interval '10 minutes'),
	('192.0.2.30',     2, interval '1 minute'),
	('192.0.2.31',     1, interval '3 minutes'),
	('2001:db8::40',   5, interval '15 minutes'),
	('2001:db8::41',   2, interval '30 minutes'),
	-- outside a 60-minute window: listed only with e.g. minutes=180
	('198.51.100.50',  6, interval '2 hours')
) AS s(ip, reporters, age)
JOIN (
	SELECT k.id, k.user_id, row_number() OVER (ORDER BY u.email) AS n
	FROM api_keys k
	JOIN users u ON u.id = k.user_id
	WHERE k.key_prefix = 'sfw_dev_'
) k ON k.n <= s.reporters;

-- Many reports from a single user still count as one distinct reporter.
INSERT INTO ip_reports (ip, api_key_id, user_id, reported_at, source)
SELECT '203.0.113.60'::inet, k.id, k.user_id, now() - g * interval '1 minute', 'auth'
FROM api_keys k
JOIN users u ON u.id = k.user_id
CROSS JOIN generate_series(1, 10) AS g
WHERE k.key_prefix = 'sfw_dev_' AND u.email = 'dev-reporter1';
