-- 0001_init: base schema for lazytg local storage.
-- All timestamps are stored as Unix seconds (INTEGER) to keep the schema
-- portable across timezones and to allow simple range queries from FTS UI.

CREATE TABLE IF NOT EXISTS accounts (
    id         INTEGER PRIMARY KEY,
    phone      TEXT    UNIQUE NOT NULL,
    alias      TEXT,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS chats (
    id                INTEGER PRIMARY KEY,
    type              TEXT    NOT NULL,
    title             TEXT,
    username          TEXT,
    last_message_date INTEGER,
    unread_count      INTEGER NOT NULL DEFAULT 0,
    pinned            INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
    id        INTEGER NOT NULL,
    chat_id   INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    from_id   INTEGER,
    date      INTEGER NOT NULL,
    text      TEXT,
    reply_to  INTEGER,
    raw_blob  BLOB,
    PRIMARY KEY (chat_id, id)
);

CREATE INDEX IF NOT EXISTS idx_messages_chat_date ON messages(chat_id, date DESC);

CREATE TABLE IF NOT EXISTS peers (
    id          INTEGER PRIMARY KEY,
    type        TEXT    NOT NULL,
    access_hash INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS state (
    account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    pts        INTEGER,
    qts        INTEGER,
    date       INTEGER,
    seq        INTEGER
);

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
