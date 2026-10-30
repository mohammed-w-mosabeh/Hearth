-- Profiles: named sets of things to start or stop together (SPEC 18,
-- ROADMAP 4.6). items and undo are JSON lists (internal/profiles.Item);
-- undo is what activation really changed, so restoring survives a restart.
CREATE TABLE profiles (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    items        TEXT    NOT NULL,
    auto_game    INTEGER NOT NULL DEFAULT 0 CHECK (auto_game IN (0, 1)),
    activated_at INTEGER,                      -- unix milliseconds, NULL when inactive
    activated_by TEXT    NOT NULL DEFAULT '',  -- user or game
    undo         TEXT    NOT NULL DEFAULT '[]'
) STRICT;
