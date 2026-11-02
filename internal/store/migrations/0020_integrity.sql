-- Integrity watch (ROADMAP 7.3): what each watched file looked like when it
-- was last accepted. text is masked (SECURITY S16i) and only for small text
-- files, so a change can be shown as a diff. The file's mtime and ctime let a
-- scan skip hashing what has not been touched.
CREATE TABLE integrity_baseline (
    path     TEXT PRIMARY KEY, -- as shown: ~/.ssh/config or /etc/cron.d/job
    root     TEXT NOT NULL,    -- the watched item it belongs to
    kind     TEXT NOT NULL,    -- file, large, link, other, unreadable or name
    hash     TEXT NOT NULL,
    size     INTEGER NOT NULL,
    mtime    INTEGER NOT NULL, -- unix ns
    ctime    INTEGER NOT NULL, -- unix ns
    text     TEXT,             -- NULL when there is nothing to diff against
    accepted INTEGER NOT NULL  -- unix ms
) STRICT;

INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('Sensitive file changed', 'integrity.changed', '', '>', 0, 0, 'warning', 1, 0);
