-- 0003_peers_extended: ensure peers table carries the metadata SendService
-- needs to build an InputPeer (type + access_hash). The base 0001 migration
-- already declares the columns, so this file is a forward-compatible no-op
-- on fresh installs and an idempotent guard on databases that may have been
-- bootstrapped by an earlier branch with a slimmer schema.
--
-- The CREATE TABLE IF NOT EXISTS lets the migration re-run cleanly; the
-- index on `type` accelerates the future "list all channels" UI lookup
-- without changing existing query plans.

CREATE TABLE IF NOT EXISTS peers (
    id          INTEGER PRIMARY KEY,
    type        TEXT    NOT NULL,
    access_hash INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_peers_type ON peers(type);
