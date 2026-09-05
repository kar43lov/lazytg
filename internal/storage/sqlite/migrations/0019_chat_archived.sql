-- 0019_chat_archived: which chats the user put in the archive.
--
-- The archive is the folder people hide chats in and forget; a client that
-- cannot show it loses those chats entirely. Telegram keeps it as folder 1
-- of messages.getDialogs, and a chat moves between it and the main list on
-- updateFolderPeers. One flag on the row: the main list and the folder
-- tabs leave archived chats out, an "Archive" tab shows them alone.

ALTER TABLE chats ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
