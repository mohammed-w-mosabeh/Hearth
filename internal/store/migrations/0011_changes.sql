-- System snapshots and the changes between them (SPEC 18, ROADMAP 4.5).
-- data is gzip-compressed JSON (internal/changes.State). Automatic
-- snapshots are stored only when something changed and pruned after 90
-- days; ones the user takes are kept until deleted.
CREATE TABLE snapshots (
    id    INTEGER PRIMARY KEY,
    ts    INTEGER NOT NULL,             -- unix milliseconds
    label TEXT    NOT NULL DEFAULT '',  -- the user's name for it, '' for automatic
    auto  INTEGER NOT NULL CHECK (auto IN (0, 1)),
    data  BLOB    NOT NULL
) STRICT;

CREATE INDEX snapshots_ts ON snapshots (ts);

-- What changed between two automatic snapshots (packages come from
-- dpkg's own log instead).
CREATE TABLE changes (
    id      INTEGER PRIMARY KEY,
    ts      INTEGER NOT NULL,  -- unix milliseconds, when it was seen
    source  TEXT    NOT NULL,  -- units, containers, listeners
    kind    TEXT    NOT NULL,  -- added, deleted, changed
    subject TEXT    NOT NULL,
    before  TEXT    NOT NULL DEFAULT '',
    after   TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX changes_ts ON changes (ts);
