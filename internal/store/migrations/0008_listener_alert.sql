-- Default rule for a new listener on every address (SPEC 15, ROADMAP 3.9).
INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('New exposed listener', 'net.new_listener', '', '>', 0, 0, 'warning', 1, 0);
