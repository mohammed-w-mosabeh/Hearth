-- Default rules for container events (ROADMAP 5.4). Log-pattern rules have
-- no default: the pattern is the user's.
INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('Container unhealthy',       'container.unhealthy', '', '>', 0, 0, 'warning', 1, 0),
    ('Container out of memory',   'container.oom',       '', '>', 0, 0, 'warning', 1, 0);
