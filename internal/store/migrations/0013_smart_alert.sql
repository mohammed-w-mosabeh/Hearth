-- Default rule for a disk that reports it is failing (SPEC 15, ROADMAP 5.3).
INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('SMART failing', 'disk.smart_failing', '', '>', 0, 0, 'critical', 1, 0);
