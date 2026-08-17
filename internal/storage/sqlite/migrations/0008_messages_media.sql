-- 0008_messages_media: extend messages with optional media metadata.
--
-- The Telegram message envelope can carry a Document (any binary attachment:
-- file, voice, video, sticker, …) or a Photo. The fields below capture the
-- subset gotd needs to reconstruct an InputDocumentFileLocation /
-- InputPhotoFileLocation for download:
--
--   media_kind          — "document" or "photo" (NULL = no media)
--   media_id            — Telegram's file ID
--   media_access_hash   — required to authenticate the download request
--   media_file_reference — opaque blob refreshed by getDifference; required
--                         and short-lived (~1 hour). Stored so the UI can
--                         retry an old download by re-fetching the parent
--                         message via messages.getMessages.
--   media_dc            — DC ID (informational; gotd handles routing)
--   media_filename      — DocumentAttributeFilename or "photo.jpg" fallback
--   media_size          — original byte size; UI displays it next to the
--                         filename so the user can decide before pressing
--                         Ctrl-D on a 2 GB blob
--   media_mime_type     — Document.MimeType; empty for photos
--   media_thumb_size    — optional photo size selector ("x", "y", …) the
--                         downloader passes to InputPhotoFileLocation; NULL
--                         for documents which always download the full file
--
-- Every column is NULL for plain text messages. Splitting media into a
-- separate table was considered but discarded: nearly every chat has at
-- least one media-message and the JOIN cost on every thread render would
-- dominate the wall-clock budget. Inline NULLs cost ~1 byte per column
-- per row in SQLite's storage format — cheaper than a JOIN.
ALTER TABLE messages ADD COLUMN media_kind          TEXT;
ALTER TABLE messages ADD COLUMN media_id            INTEGER;
ALTER TABLE messages ADD COLUMN media_access_hash   INTEGER;
ALTER TABLE messages ADD COLUMN media_file_reference BLOB;
ALTER TABLE messages ADD COLUMN media_dc            INTEGER;
ALTER TABLE messages ADD COLUMN media_filename      TEXT;
ALTER TABLE messages ADD COLUMN media_size          INTEGER;
ALTER TABLE messages ADD COLUMN media_mime_type     TEXT;
ALTER TABLE messages ADD COLUMN media_thumb_size    TEXT;
