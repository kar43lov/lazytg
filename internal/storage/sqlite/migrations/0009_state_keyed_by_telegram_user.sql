-- 0009_state_keyed_by_telegram_user: key update state by the Telegram user id
-- instead of the local accounts row, and drop the foreign key that made the
-- table unwritable.
--
-- gotd's updates.StateStorage is keyed by the Telegram user id — every call
-- arrives as SetState(userID, …) / GetState(userID). Both state tables named
-- that column `account_id` and declared it REFERENCES accounts(id), where
-- accounts.id is a local autoincrement rowid. The two identifier spaces never
-- coincide, so every write failed with FOREIGN KEY constraint failed (787),
-- updates.Manager refused to start, and gap recovery silently did not exist:
--
--   tui: update gap recovery stopped — live updates continue without it
--   err: set state user=8385473863: FOREIGN KEY constraint failed (787)
--
-- The unit tests missed it because they seed an accounts row whose id equals
-- the user id they then pass in; only a real login makes the two diverge.
--
-- Accounts are separated by data directory (the --account flag picks one), so
-- a database only ever holds one account's state — the same scoping rule
-- channelAccessHasher already documents. The foreign key encoded a constraint
-- the storage layer cannot satisfy and does not need.
--
-- Rebuild rather than ALTER: SQLite cannot drop a column constraint in place.
-- Rows are copied for completeness, though in practice the tables are empty
-- everywhere — nothing could ever be written to them.

CREATE TABLE IF NOT EXISTS state_v9 (
    user_id INTEGER PRIMARY KEY,
    pts     INTEGER,
    qts     INTEGER,
    date    INTEGER,
    seq     INTEGER
);

INSERT OR IGNORE INTO state_v9 (user_id, pts, qts, date, seq)
    SELECT account_id, pts, qts, date, seq FROM state;

DROP TABLE state;
ALTER TABLE state_v9 RENAME TO state;

CREATE TABLE IF NOT EXISTS channel_state_v9 (
    user_id    INTEGER NOT NULL,
    channel_id INTEGER NOT NULL,
    pts        INTEGER NOT NULL,
    PRIMARY KEY (user_id, channel_id)
);

INSERT OR IGNORE INTO channel_state_v9 (user_id, channel_id, pts)
    SELECT account_id, channel_id, pts FROM channel_state;

DROP TABLE channel_state;
ALTER TABLE channel_state_v9 RENAME TO channel_state;
