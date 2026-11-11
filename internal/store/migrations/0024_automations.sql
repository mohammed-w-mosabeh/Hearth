-- Automations (ROADMAP 12.1, SECURITY S15d): WHEN a trigger fires, IF
-- conditions hold, THEN run registered actions. spec is validated by
-- internal/automations/spec before it is stored, and again when read.
CREATE TABLE automations (
    id      INTEGER PRIMARY KEY,
    name    TEXT    NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    spec    TEXT    NOT NULL CHECK (json_valid(spec)),
    origin  TEXT    NOT NULL,        -- ui, template:<id>, import, alert, mode:<file>#<n>@<hash>
    created INTEGER NOT NULL,        -- unix ms
    updated INTEGER NOT NULL
) STRICT;

-- One row per run: what fired it, whether the conditions held, each
-- step's result. Cleaned and masked before they are stored (S19).
CREATE TABLE automation_runs (
    id            INTEGER PRIMARY KEY,
    automation_id INTEGER NOT NULL,
    started       INTEGER NOT NULL,
    finished      INTEGER NOT NULL DEFAULT 0,
    state         TEXT    NOT NULL,  -- running, done, failed, skipped, waiting, limited
    record        TEXT    NOT NULL CHECK (json_valid(record))
) STRICT;

CREATE INDEX automation_runs_by ON automation_runs (automation_id, started);
CREATE INDEX automation_runs_started ON automation_runs (started);

-- Steps waiting for the user's click (ROADMAP 12.6).
CREATE TABLE automation_approvals (
    id      INTEGER PRIMARY KEY,
    run_id  INTEGER NOT NULL,
    created INTEGER NOT NULL,
    expires INTEGER NOT NULL,
    state   TEXT    NOT NULL,        -- pending, approved, dismissed, expired
    steps   TEXT    NOT NULL CHECK (json_valid(steps))
) STRICT;
