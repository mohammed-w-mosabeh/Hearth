package profiles

import (
	"context"
	"testing"
)

// gameTrigger plays the game trigger's automation (ROADMAP 12.7): an app
// started trigger with the game detector, which fires when a game appears
// and runs mode/activate, that is AutoActivate by "game".
type gameTrigger struct {
	m       *Manager
	running bool
}

func (g *gameTrigger) tick(ctx context.Context, t *testing.T) {
	t.Helper()
	games, err := g.m.cfg.Playing()
	on := err == nil && len(games) > 0
	if on && !g.running {
		list, err := g.m.cfg.Store.Profiles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			if p.AutoGame && p.ActivatedAt.IsZero() {
				if err := g.m.AutoActivate(ctx, p.ID, ByGame, "a game started"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	g.running = on
}

// tick is one look at games: the game trigger's automation, then the
// manager's own poll, which restores what a game switched on once it ends.
func (f *fixture) tick(ctx context.Context, t *testing.T) {
	t.Helper()
	if f.game == nil {
		f.game = &gameTrigger{m: f.m}
	}
	f.game.tick(ctx, t)
	f.m.poll(ctx)
}
