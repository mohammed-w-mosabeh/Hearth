-- UI and user preferences as small JSON documents (e.g. the Home layout).
CREATE TABLE settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL CHECK (json_valid(value)),
    updated_at INTEGER NOT NULL               -- unix milliseconds
) STRICT, WITHOUT ROWID;
