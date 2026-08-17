-- 0007_files: dedup cache for media downloaded by Stage 3 file pipeline.
--
-- The table is keyed by Telegram's file ID (document or photo). When the user
-- presses Ctrl-D twice on the same media, DownloadService consults this row
-- and returns the cached path instead of re-downloading the same bytes — a
-- gigabyte over a slow link costs the user real time and bandwidth.
--
-- access_hash is recorded so a future migration to a stronger key (e.g.
-- (id, access_hash) pair to disambiguate Telegram's per-DC file_id reuse)
-- can run without losing the existing cache: the column is already there.
--
-- The path column is the on-disk location chosen by FileStore — typically
-- ~/Downloads/lazytg/<chat_title>/<filename>. We record the user-visible
-- path (not a content-addressed blob) so the path printed back to the user
-- matches what they would see in Finder / nautilus when they open
-- ~/Downloads.
--
-- size is duplicated from the gotd download response so the cache row can
-- answer "is the on-disk file complete?" without re-stat'ing the path on
-- every check (the file may have been moved or deleted by the user).
--
-- downloaded_at is Unix seconds, matching the rest of the schema.
CREATE TABLE IF NOT EXISTS downloaded_files (
    file_id       INTEGER PRIMARY KEY,
    access_hash   INTEGER,
    path          TEXT NOT NULL,
    size          INTEGER,
    downloaded_at INTEGER NOT NULL
);
