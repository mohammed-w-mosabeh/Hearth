-- ROADMAP 6.8: a tile's check may be a TCP connect to this port of the
-- URL's host; 0 is the web check of the URL itself.
ALTER TABLE homepage_tiles ADD COLUMN check_port INTEGER NOT NULL DEFAULT 0 CHECK (check_port BETWEEN 0 AND 65535);
