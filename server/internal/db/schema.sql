-- Requires extension for UUID generation
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS api_keys (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_prefix  TEXT NOT NULL,      -- first 8 chars of the key, for fast lookup
    key_hash    TEXT UNIQUE NOT NULL, -- sha256 of the full key
    name        TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys(key_prefix) WHERE revoked_at IS NULL;
-- lists a user's keys and counts active keys; also speeds up the cascade from users
CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id);
-- limit of 10 active keys per user is enforced in application code (transaction + COUNT)

CREATE TABLE IF NOT EXISTS ip_reports (
    id          BIGSERIAL PRIMARY KEY,
    ip          INET NOT NULL,
    api_key_id  UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- fast counting of reports for a given IP within a time window
CREATE INDEX IF NOT EXISTS idx_ip_reports_ip_time ON ip_reports(ip, reported_at);

-- statistics (UI) count reports by time alone
CREATE INDEX IF NOT EXISTS idx_ip_reports_time ON ip_reports(reported_at);

-- per-user and per-key report counts; also speeds up the cascade on user/key delete
CREATE INDEX IF NOT EXISTS idx_ip_reports_user ON ip_reports(user_id);
CREATE INDEX IF NOT EXISTS idx_ip_reports_api_key ON ip_reports(api_key_id);

-- what detected the attack, e.g. 'auth' or 'fail2ban' (reporter plugin name);
-- NULL for reports sent without it
ALTER TABLE ip_reports ADD COLUMN IF NOT EXISTS source TEXT;

-- Existing databases: make deleting a user (or key) cascade to its reports.
-- Only touches constraints that are not yet ON DELETE CASCADE.
DO $$
DECLARE c record;
BEGIN
    FOR c IN
        SELECT conname, confrelid::regclass AS ref, a.attname
        FROM pg_constraint k
        JOIN pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = k.conkey[1]
        WHERE k.conrelid = 'ip_reports'::regclass AND k.contype = 'f' AND k.confdeltype <> 'c'
    LOOP
        EXECUTE format('ALTER TABLE ip_reports DROP CONSTRAINT %I, ADD CONSTRAINT %I FOREIGN KEY (%I) REFERENCES %s(id) ON DELETE CASCADE',
                       c.conname, c.conname, c.attname, c.ref);
    END LOOP;
END $$;

CREATE TABLE IF NOT EXISTS blacklist (
    ip                 INET PRIMARY KEY,
    first_seen         TIMESTAMPTZ NOT NULL,
    last_seen          TIMESTAMPTZ NOT NULL,
    distinct_reporters INT NOT NULL
);

-- Admin flag for users; existing databases get it via ALTER below.
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT FALSE;

-- The pruner deletes rows every hour; vacuum sooner than the 20% default so the
-- table does not grow with dead rows.
ALTER TABLE ip_reports SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);