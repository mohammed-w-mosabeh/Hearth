-- Uninstall completely (ROADMAP 11.7, SECURITY S16k): one row per
-- operation, kept 7 days for Recently removed. record is the operation as
-- JSON: the plan the user confirmed, each step's outcome, and what undo
-- needs (package versions, trash URIs, the broker's archive, Hearth's
-- references as they were).
CREATE TABLE uninstall_ops (
    id      TEXT    PRIMARY KEY,               -- 32 hex digits
    item    TEXT    NOT NULL,                  -- the item ID, e.g. "app:vlc"
    name    TEXT    NOT NULL,                  -- shown
    created INTEGER NOT NULL,                  -- unix ms
    updated INTEGER NOT NULL,                  -- unix ms
    state   TEXT    NOT NULL,                  -- running, done, failed, restored
    record  TEXT    NOT NULL CHECK (json_valid(record))
) STRICT;

CREATE INDEX uninstall_ops_created ON uninstall_ops (created);
