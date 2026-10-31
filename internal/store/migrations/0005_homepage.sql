-- Homepage tiles (SPEC 6) and uploaded icons.
CREATE TABLE homepage_tiles (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    url          TEXT    NOT NULL,             -- http(s), validated by internal/homepage
    description  TEXT    NOT NULL DEFAULT '',
    icon         TEXT    NOT NULL DEFAULT '',  -- "builtin:<name>", "upload:<sha256>" or ''
    grp          TEXT    NOT NULL DEFAULT '',  -- group name, '' for none
    container    TEXT    NOT NULL DEFAULT '',  -- linked container name, '' for none
    health_check INTEGER NOT NULL CHECK (health_check IN (0, 1)),
    position     INTEGER NOT NULL,             -- order within the page
    updated_at   INTEGER NOT NULL              -- unix milliseconds
) STRICT;

-- Icons as uploaded after checks (PNG, WebP) or sanitizing (SVG), keyed
-- by the SHA-256 of what is stored. Unused ones are deleted with their
-- last tile.
CREATE TABLE homepage_icons (
    sha        TEXT    PRIMARY KEY,
    mime       TEXT    NOT NULL CHECK (mime IN ('image/png', 'image/webp', 'image/svg+xml')),
    data       BLOB    NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
