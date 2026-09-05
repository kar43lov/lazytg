-- 0018_message_origin: where a forwarded message came from, and whether
-- a message is pinned.
--
-- A forwarded message drawn as the forwarder's own words misattributes
-- them; every client draws "forwarded from X" above it. The origin rides
-- on the message (fwd_from) and is stored as a small JSON object — name,
-- id when not hidden, date — shaped in internal/core/domain/forward.go.
--
-- pinned is the message's own flag on the wire, moved by
-- updatePinnedMessages / updatePinnedChannelMessages; the thread shows
-- the newest pinned message it has in a bar above the conversation.

ALTER TABLE messages ADD COLUMN forward TEXT    NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN pinned  INTEGER NOT NULL DEFAULT 0;
