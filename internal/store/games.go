package store

import (
	"context"
	"fmt"
	"time"
)

// Playtimes returns the tracked seconds per game ID.
func (db *DB) Playtimes(ctx context.Context) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, seconds FROM game_playtime`)
	if err != nil {
		return nil, fmt.Errorf("store: playtimes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var s int64
		if err := rows.Scan(&id, &s); err != nil {
			return nil, fmt.Errorf("store: playtimes: %w", err)
		}
		out[id] = s
	}
	return out, rows.Err()
}

// AddPlaytime adds seconds to game id, played at at.
func (db *DB) AddPlaytime(ctx context.Context, id string, seconds int64, at time.Time) error {
	if seconds < 0 {
		return fmt.Errorf("store: add playtime: negative seconds")
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO game_playtime (id, seconds, last_played) VALUES (?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET seconds = seconds + excluded.seconds, last_played = excluded.last_played`,
		id, seconds, at.Unix())
	if err != nil {
		return fmt.Errorf("store: add playtime: %w", err)
	}
	return nil
}
