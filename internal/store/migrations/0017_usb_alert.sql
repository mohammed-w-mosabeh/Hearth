-- Default rule for a USB device never seen here (ROADMAP 6.7).
INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('New USB device', 'usb.new_device', '', '>', 0, 0, 'info', 1, 0);
