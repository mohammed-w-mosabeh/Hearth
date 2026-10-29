-- Hourly CPU and memory per app, service and container (ROADMAP 4.3,
-- reports). Only active items are kept, at most 100 per hour, 90 days.
CREATE TABLE item_usage (
    item    TEXT    NOT NULL,  -- process group ID, e.g. "unit:cups.service"
    ts      INTEGER NOT NULL,  -- hour start, unix seconds
    kind    TEXT    NOT NULL,
    name    TEXT    NOT NULL,
    cpu_avg REAL    NOT NULL,  -- % of the machine
    cpu_max REAL    NOT NULL,
    mem_avg REAL    NOT NULL,  -- bytes
    mem_max REAL    NOT NULL,
    n       INTEGER NOT NULL CHECK (n > 0),  -- samples in the hour
    PRIMARY KEY (item, ts)
) STRICT, WITHOUT ROWID;

CREATE INDEX item_usage_ts ON item_usage (ts);
