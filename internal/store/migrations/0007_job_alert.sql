-- Default rule for scheduled jobs that fail and ask to be notified
-- (SPEC 14/15, ROADMAP 3.8).
INSERT INTO alert_rules (name, metric, target, op, threshold, for_s, severity, enabled, updated_at) VALUES
    ('Scheduled job failed', 'job.failed', '', '>', 0, 0, 'warning', 1, 0);
