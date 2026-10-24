-- Downsampled metric history (SPEC 8: 1 min for 7 days, 1 h for 1 year).
-- ts is the bucket start in unix seconds; n is how many raw samples it holds.
CREATE TABLE metrics_1m (
    metric TEXT    NOT NULL,
    ts     INTEGER NOT NULL,
    avg    REAL    NOT NULL,
    min    REAL    NOT NULL,
    max    REAL    NOT NULL,
    n      INTEGER NOT NULL CHECK (n > 0),
    PRIMARY KEY (metric, ts)
) STRICT, WITHOUT ROWID;

CREATE TABLE metrics_1h (
    metric TEXT    NOT NULL,
    ts     INTEGER NOT NULL,
    avg    REAL    NOT NULL,
    min    REAL    NOT NULL,
    max    REAL    NOT NULL,
    n      INTEGER NOT NULL CHECK (n > 0),
    PRIMARY KEY (metric, ts)
) STRICT, WITHOUT ROWID;

-- Retention deletes by time across all metrics.
CREATE INDEX metrics_1m_ts ON metrics_1m (ts);
CREATE INDEX metrics_1h_ts ON metrics_1h (ts);
