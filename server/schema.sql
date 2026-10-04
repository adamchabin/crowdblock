-- Requires extension for UUID generation
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
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
-- limit of 10 active keys per user is enforced in application code (transaction + COUNT)

CREATE TABLE IF NOT EXISTS ip_reports (
    id          BIGSERIAL PRIMARY KEY,
    ip          INET NOT NULL,
    api_key_id  UUID NOT NULL REFERENCES api_keys(id),
    user_id     UUID NOT NULL REFERENCES users(id),
    reported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- fast counting of reports for a given IP within a time window
CREATE INDEX IF NOT EXISTS idx_ip_reports_ip_time ON ip_reports(ip, reported_at);

-- what detected the attack, e.g. 'auth' or 'fail2ban' (reporter plugin name);
-- NULL for reports sent without it
ALTER TABLE ip_reports ADD COLUMN IF NOT EXISTS source TEXT;

CREATE TABLE IF NOT EXISTS blacklist (
    ip                 INET PRIMARY KEY,
    first_seen         TIMESTAMPTZ NOT NULL,
    last_seen          TIMESTAMPTZ NOT NULL,
    distinct_reporters INT NOT NULL
);