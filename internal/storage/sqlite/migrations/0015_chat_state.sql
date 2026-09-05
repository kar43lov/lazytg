-- 0015_chat_state: what the chat list knows on the phone and did not here.
--
-- Four facts about a dialog that every official client shows in the list
-- and this one dropped on the way in: whether it is muted, whether the user
-- marked it unread by hand, and — for a person — whether they are online
-- right now and when they were last. All four arrive with the dialog page
-- the sync already fetches and change through updates the dispatcher
-- already receives, so keeping them costs no request.
--
-- muted_until is a unix timestamp, zero for "not muted"; Telegram's "mute
-- forever" is a date in 2038, stored as it comes. last_seen is a unix
-- timestamp, zero for "unknown" — which is also what Telegram reports for
-- people who hide it, and the list shows nothing rather than guessing.

ALTER TABLE chats ADD COLUMN muted_until INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN unread_mark INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN online      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN last_seen   INTEGER NOT NULL DEFAULT 0;
