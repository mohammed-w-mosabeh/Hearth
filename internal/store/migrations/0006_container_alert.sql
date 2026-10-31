-- Default rule for containers that stop on their own (SPEC 15, ROADMAP 2.11).
INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('Container stopped', 'container.exited', '', '>', 0, 0, 'warning', 1, 0);
