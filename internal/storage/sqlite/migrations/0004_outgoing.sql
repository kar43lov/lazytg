-- 0004_outgoing: local-first store for messages the user has composed but
-- whose server fate (delivered / awaiting / failed) is not yet known.
--
-- The optimistic-UI flow keeps every send in this table from the moment the
-- user presses Enter, so reopening lazytg surfaces in-flight or failed
-- messages instead of silently dropping them. server_id is filled when
-- Telegram confirms the message and acts as the join key into messages(id).
-- error carries the human-readable failure reason for state="failed".
--
-- created_at is a Unix-seconds timestamp; the order matches send order so
-- chats(chat_id, created_at) gives us a cheap "show me my pending sends"
-- query without a join through messages.

CREATE TABLE IF NOT EXISTS outgoing (
    local_id    TEXT    PRIMARY KEY,
    chat_id     INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    text        TEXT    NOT NULL,
    reply_to    INTEGER NOT NULL DEFAULT 0,
    state       TEXT    NOT NULL,
    server_id   INTEGER,
    error       TEXT,
    created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_outgoing_chat_created ON outgoing(chat_id, created_at);
CREATE INDEX IF NOT EXISTS idx_outgoing_state ON outgoing(state);
