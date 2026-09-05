-- 0014_message_entities: the formatting Telegram attaches to a message.
--
-- Bold, italic, code, a link behind a word, a spoiler — Telegram carries all
-- of it as a list of spans next to the plain text, and a client that keeps
-- only the text shows every message flattened: a code block becomes prose,
-- a strikethrough becomes an assertion, and "click here" loses the address
-- it pointed at, which is also the one thing about a link a reader should
-- be able to see before following it.
--
-- Stored as JSON in one column, for the reasons the reactions column gives:
-- the thread reads a page of messages and renders each row whole, and a
-- side table would put a join on that path to normalise a list nothing
-- queries across messages. Offsets are stored in runes, converted once at
-- the edge that talks to Telegram, so no reader of this column has to know
-- that the wire counts UTF-16 units.
--
-- Existing rows default to the empty string, which reads as "no formatting"
-- and is also what an unformatted message stores. They are not backfilled:
-- an ordinary backfill re-fetches history and writes the spans, and spending
-- requests to colour in old rows is not worth doing on an account watched
-- for running an unofficial client.

ALTER TABLE messages ADD COLUMN entities TEXT NOT NULL DEFAULT '';

-- edit_date is when the message was last rewritten, as a unix timestamp,
-- zero when it never was. It travels with the same edit that made edits
-- from other devices visible at all: a message that changed under the
-- reader should say so, the way every official client does.
ALTER TABLE messages ADD COLUMN edit_date INTEGER NOT NULL DEFAULT 0;
