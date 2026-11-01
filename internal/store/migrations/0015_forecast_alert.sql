-- Default rule for a disk forecast to fill within 7 days (ROADMAP 6.1).
INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('Disk filling up', 'disk.forecast_full', '', '>', 0, 0, 'warning', 1, 0);
