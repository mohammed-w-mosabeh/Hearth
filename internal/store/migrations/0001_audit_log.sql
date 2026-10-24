-- Append-only audit log (SECURITY S18).
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,             -- unix milliseconds
    origin      TEXT    NOT NULL CHECK (origin IN ('local', 'phone', 'broker')),
    uid         INTEGER NOT NULL,
    action      TEXT    NOT NULL,             -- e.g. "service.restart"
    target      TEXT    NOT NULL,             -- item ID
    params_hash TEXT    NOT NULL,             -- hex SHA-256 of canonical params JSON
    result      TEXT    NOT NULL              -- "ok" or an error summary
) STRICT;

CREATE INDEX audit_log_ts ON audit_log (ts);

CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
