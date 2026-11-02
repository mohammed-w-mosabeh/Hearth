-- Lynis (ROADMAP 7.1): the hardening index of each finished audit, and a
-- default rule for when it drops. Only figures: findings stay in the report
-- the broker holds.
CREATE TABLE lynis_runs (
    time        INTEGER PRIMARY KEY, -- unix ms the run finished
    hardening   INTEGER NOT NULL CHECK (hardening BETWEEN 0 AND 100),
    warnings    INTEGER NOT NULL,
    suggestions INTEGER NOT NULL
) STRICT;

INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('Hardening index dropped', 'lynis.index_drop', '', '>', 0, 0, 'warning', 1, 0);
