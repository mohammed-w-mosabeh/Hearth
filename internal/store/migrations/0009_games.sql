-- Playtime Hearth tracked for desktop games (SPEC 7, ROADMAP 4.1).
CREATE TABLE game_playtime (
    id          TEXT    PRIMARY KEY,  -- discovery game ID, "desktop.<app id>"
    seconds     INTEGER NOT NULL CHECK (seconds >= 0),
    last_played INTEGER NOT NULL      -- unix seconds
) STRICT;
