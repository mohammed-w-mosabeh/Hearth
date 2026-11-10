-- Modes (SPEC 23, ROADMAP 10.1). A mode's definition lives in a TOML file
-- the user edits (~/.config/hearth/modes/<id>.toml), which is untrusted. What
-- a file must not be able to fake lives here instead (SECURITY S15b): which
-- triggers are armed, which mode is active and what restores it, and the
-- approvals waiting for a click. Each row records the file's sha256, so a
-- changed file no longer matches what was armed or approved.

-- A trigger is armed only by a click in Hearth. trigger_idx is its index in
-- the file; a different hash means the file changed and it is disarmed.
CREATE TABLE mode_arming (
    mode_id     TEXT    NOT NULL,
    trigger_idx INTEGER NOT NULL CHECK (trigger_idx >= 0),
    file_hash   TEXT    NOT NULL,
    armed_at    INTEGER NOT NULL, -- unix ms
    PRIMARY KEY (mode_id, trigger_idx)
) STRICT;

-- At most one mode is active. snapshot is JSON: what the mode really changed
-- and the values to put back, so revert survives a restart.
CREATE TABLE mode_active (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    mode_id      TEXT    NOT NULL,
    file_hash    TEXT    NOT NULL,
    since        INTEGER NOT NULL, -- unix ms
    activated_by TEXT    NOT NULL, -- user, or the trigger kind that switched it
    snapshot     TEXT    NOT NULL DEFAULT '{}'
) STRICT;

-- Stack actions an automatic trigger could not run unattended. actions is
-- JSON (stack names and verbs); nothing runs until the user approves.
CREATE TABLE mode_approvals (
    id         INTEGER PRIMARY KEY,
    mode_id    TEXT    NOT NULL,
    file_hash  TEXT    NOT NULL,
    reason     TEXT    NOT NULL, -- the trigger kind
    actions    TEXT    NOT NULL,
    created_at INTEGER NOT NULL, -- unix ms
    state      TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'dismissed', 'expired')),
    decided_at INTEGER           -- unix ms, NULL while pending
) STRICT;
CREATE INDEX mode_approvals_pending ON mode_approvals (state, created_at);
