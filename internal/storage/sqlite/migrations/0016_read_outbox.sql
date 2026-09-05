-- 0016_read_outbox: how far the other side has read.
--
-- Every official client draws one tick on a message you sent and two once
-- it was read, and the fact behind the second tick is a single number per
-- chat: the id of the newest outgoing message the other party has seen.
-- It arrives with the dialog page (read_outbox_max_id) and moves through
-- updateReadHistoryOutbox / updateReadChannelOutbox, both of which the
-- dispatcher already receives. Stored monotonically — a read pointer only
-- ever moves forward, and an update that arrived out of order must not
-- un-read a message.

ALTER TABLE chats ADD COLUMN read_outbox_max_id INTEGER NOT NULL DEFAULT 0;
