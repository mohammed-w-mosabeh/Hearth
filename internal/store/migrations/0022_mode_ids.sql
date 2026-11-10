-- Profiles become modes (ROADMAP 10.2). Several modes may be active at once,
-- as profiles could be, so mode_active holds one row per active mode;
-- 0021's table was never written to.
DROP TABLE mode_active;
CREATE TABLE mode_active (
    mode_id      TEXT    PRIMARY KEY,
    file_hash    TEXT    NOT NULL,
    since        INTEGER NOT NULL, -- unix ms
    activated_by TEXT    NOT NULL, -- user, or the trigger kind that switched it
    snapshot     TEXT    NOT NULL DEFAULT '{}'
) STRICT;

-- Each mode file has a number, used by the profile actions and the audit
-- log. A migrated profile keeps its ID, so history still names it. Numbers
-- are never reused (AUTOINCREMENT), and a file's number survives its
-- deletion, so a later file of that name is the same mode in history.
CREATE TABLE mode_ids (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    mode_id TEXT    NOT NULL UNIQUE
);

-- New modes are numbered after every profile, so a profile migrated later
-- keeps its ID without meeting a mode that took it.
INSERT INTO sqlite_sequence (name, seq) SELECT 'mode_ids', COALESCE(MAX(id), 0) FROM profiles;
