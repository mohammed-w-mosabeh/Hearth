-- Automations write the audit log as "automation:<id>" (SECURITY S15d,
-- S18), which the origin check of 0001 refused: unattended steps could not
-- be audited, so they failed. SQLite cannot change a CHECK in place, so the
-- table is rebuilt with every row, its index and its append-only triggers.
CREATE TABLE audit_log_new (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,             -- unix milliseconds
    origin      TEXT    NOT NULL CHECK (origin IN ('local', 'phone', 'broker')
                                        OR (origin GLOB 'automation:[1-9]*' AND substr(origin, 12) NOT GLOB '*[^0-9]*')),
    uid         INTEGER NOT NULL,
    action      TEXT    NOT NULL,             -- e.g. "service.restart"
    target      TEXT    NOT NULL,             -- item ID
    params_hash TEXT    NOT NULL,             -- hex SHA-256 of canonical params JSON
    result      TEXT    NOT NULL              -- "ok" or an error summary
) STRICT;

INSERT INTO audit_log_new (id, ts, origin, uid, action, target, params_hash, result)
    SELECT id, ts, origin, uid, action, target, params_hash, result FROM audit_log;

DROP TABLE audit_log;
ALTER TABLE audit_log_new RENAME TO audit_log;

CREATE INDEX audit_log_ts ON audit_log (ts);

CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
