-- 0017_message_buttons: the keyboard a bot puts under a message.
--
-- A bot's message with buttons that cannot be pressed is a form with the
-- submit button torn off. The keyboard arrives with the message
-- (reply_markup) and changes when the bot edits the message, which is how
-- most bots answer a press, so it is stored with the row and rewritten on
-- every upsert. JSON, one row of buttons per array, empty for none — the
-- shape lives in internal/core/domain/buttons.go, shared with the search
-- window reader for the same reason the reactions column is.

ALTER TABLE messages ADD COLUMN buttons TEXT NOT NULL DEFAULT '';
