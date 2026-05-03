-- 0006_frecency: chat-level frecency table for the command palette L1.
--
-- The score column stores the precomputed `visit_count * exp(-age_days / 30)`
-- value so the hot path (TopFrecency at every Ctrl-Space) is a cheap index
-- range scan instead of a per-row exp() recomputation across thousands of
-- rows. The score becomes stale relative to "now" between visits, but that
-- is acceptable: the relative ordering only shifts when the user actually
-- visits a chat, and RecordVisit pays the recompute cost at that moment.
--
-- visit_count and last_visit are kept as separate columns (not folded into
-- score) so a future migration can switch to a different decay curve
-- without losing the underlying signal.
--
-- The chats(id) FK keeps the table consistent with the rest of the schema:
-- when a chat row is deleted (e.g. user leaves a group and we prune the
-- cache) the frecency entry vanishes too, so TopFrecency cannot return a
-- dangling chat_id.
CREATE TABLE IF NOT EXISTS chat_frecency (
    chat_id     INTEGER PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
    visit_count INTEGER NOT NULL DEFAULT 0,
    last_visit  INTEGER NOT NULL DEFAULT 0,
    score       REAL    NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_chat_frecency_score ON chat_frecency(score DESC);
