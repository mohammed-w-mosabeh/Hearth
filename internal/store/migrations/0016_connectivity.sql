-- Connectivity (ROADMAP 6.5): bytes per interface per local day, measured
-- from the kernel's counters, and the outages the checks saw. Rules for an
-- outage and a data cap do nothing until the user turns the checks on or
-- sets a cap.
CREATE TABLE net_usage (
    iface TEXT    NOT NULL,
    day   TEXT    NOT NULL, -- YYYY-MM-DD, local
    rx    INTEGER NOT NULL,
    tx    INTEGER NOT NULL,
    PRIMARY KEY (iface, day)
) STRICT, WITHOUT ROWID;

CREATE TABLE net_outages (
    id         INTEGER PRIMARY KEY,
    started_at INTEGER NOT NULL, -- unix ms
    ended_at   INTEGER,
    cause      TEXT    NOT NULL
) STRICT;

INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('Internet down',  'net.internet_down', '', '>', 0, 0, 'warning', 1, 0),
    ('Data cap reached', 'net.data_cap',    '', '>', 0, 0, 'warning', 1, 0);
