-- 0002_channel_state: per-channel pts state for gotd updates.Manager.
-- updates.Manager keeps each channel's last-seen pts so reconnects can ask
-- the server only for the difference instead of re-pulling history. It is
-- separate from `state` (which tracks the user-wide pts/qts/date/seq) so
-- that wiping a single channel's state does not invalidate the user state.

CREATE TABLE IF NOT EXISTS channel_state (
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL,
    pts        INTEGER NOT NULL,
    PRIMARY KEY (account_id, channel_id)
);
