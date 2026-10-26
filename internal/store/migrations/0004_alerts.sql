-- Alert rules and incidents (SPEC 15).
CREATE TABLE alert_rules (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    metric      TEXT    NOT NULL,             -- e.g. "disk.used_pct", see internal/alerts
    target      TEXT    NOT NULL DEFAULT '',  -- one instance (a mount, a unit...) or '' for any
    op          TEXT    NOT NULL CHECK (op IN ('>', '<')),
    threshold   REAL    NOT NULL,
    for_s       INTEGER NOT NULL CHECK (for_s >= 0),
    severity    TEXT    NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    enabled     INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    quiet_from  TEXT    NOT NULL DEFAULT '',  -- "HH:MM" local time, '' for none
    quiet_to    TEXT    NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL              -- unix milliseconds
) STRICT;

-- One row per incident: opened when a rule fires for an instance, closed
-- (ended_at) when it resolves. Rule fields are copied so history stays
-- readable after a rule is edited or deleted.
CREATE TABLE alert_events (
    id          INTEGER PRIMARY KEY,
    rule_id     INTEGER NOT NULL,
    rule_name   TEXT    NOT NULL,
    metric      TEXT    NOT NULL,
    instance    TEXT    NOT NULL,
    severity    TEXT    NOT NULL,
    op          TEXT    NOT NULL,
    threshold   REAL    NOT NULL,
    value       REAL    NOT NULL,             -- when it fired
    peak        REAL    NOT NULL,             -- worst value while open
    started_at  INTEGER NOT NULL,             -- unix milliseconds
    ended_at    INTEGER,                      -- NULL while open
    notified    INTEGER NOT NULL CHECK (notified IN (0, 1))
) STRICT;

CREATE INDEX alert_events_started ON alert_events (started_at);
CREATE INDEX alert_events_open ON alert_events (ended_at) WHERE ended_at IS NULL;

-- Default rules (SPEC 15). Container, SMART, scheduled job and listener
-- rules arrive with those features.
INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('Disk almost full',      'disk.used_pct',       '', '>', 90, 60,  'warning',  1, 0),
    ('Memory almost full',    'mem.used_pct',        '', '>', 90, 300, 'warning',  1, 0),
    ('CPU busy',              'cpu.usage',           '', '>', 95, 180, 'warning',  1, 0),
    ('CPU too hot',           'cpu.temp',            '', '>', 90, 60,  'critical', 1, 0),
    ('GPU too hot',           'gpu.temp',            '', '>', 90, 60,  'critical', 1, 0),
    ('Battery low',           'battery.discharging', '', '<', 15, 0,   'warning',  1, 0),
    ('Service failed',        'unit.failed',         '', '>', 0,  0,   'warning',  1, 0);
