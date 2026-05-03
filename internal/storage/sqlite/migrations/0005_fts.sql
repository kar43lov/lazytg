-- 0005_fts: FTS5 trigram index over messages.text + sync triggers.
--
-- The trigram tokenizer is language-agnostic (no stemming/stopwords) which
-- matters for a Telegram client where chat texts mix Russian, English and
-- emoji freely. FTS5 spike (sqlite/fts5_spike_test.go) already proved the
-- modernc.org/sqlite build supports `tokenize='trigram'` on this platform.
--
-- Standalone FTS5 (no `content=` parameter) is used deliberately: an
-- external content table reports every messages.rowid as "indexed" via
-- SELECT FROM messages_fts even when the FTS posting list is empty,
-- breaking the "is this row already indexed?" check that Backfill relies
-- on for idempotent upgrades. Standalone mode duplicates `text` into the
-- FTS table itself (≈+1× raw text), which falls comfortably inside the
-- 3-5× overhead budget the plan already accounts for.
--
-- case_sensitive=0 makes the trigram tokenizer fold case at index time so
-- that `MATCH 'при'` finds "Привет" without forcing every caller to
-- pre-lowercase its query. Diacritic stripping is left at the default
-- (0): Russian text rarely carries combining marks, and stripping would
-- alter meaning for the few words that do (e.g. "ёлка" vs "елка").
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    text,
    tokenize='trigram case_sensitive 0'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;

CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
    DELETE FROM messages_fts WHERE rowid = old.rowid;
END;

CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
    UPDATE messages_fts SET text = new.text WHERE rowid = old.rowid;
END;
