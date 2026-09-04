-- 0011_messages_media_duration: how long a playable attachment runs.
--
-- Until now every attachment that was not a photo was stored as "document",
-- because that is how MTProto transports it. That is true of the wire and
-- useless to a reader: a voice message, a round video message (кружочек), a
-- sticker and a PDF all rendered as the same grey badge, and one that often
-- carried no name at all — Telegram sends no filename attribute for a voice
-- message or a video note, so the badge read "document_5123.bin".
--
-- The kind itself needs no schema change: media_kind is TEXT and simply gains
-- values (video, video_note, voice, audio, sticker, animation). Duration does,
-- and it is the field that makes the badge worth reading — "voice, 0:42" tells
-- the user whether to spend the download, "[📎 document_5123.bin]" does not.
--
-- Existing rows keep media_kind = 'document' and duration 0. They are not
-- rewritten: the classification lives in the message as it arrives from
-- Telegram, and re-deriving it would mean re-fetching history for a cosmetic
-- gain. A row refreshed by an ordinary backfill picks up both fields.

ALTER TABLE messages ADD COLUMN media_duration INTEGER NOT NULL DEFAULT 0;
