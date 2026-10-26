package services

import (
	"context"
	"log/slog"
	"time"
)

// FailedPoller reports failed units, system and user, to alert rules
// ("unit.failed"). It keeps one connection per bus and asks every
// Interval: ListUnitsFiltered is a few milliseconds, cheaper than
// following every unit's signals while the Services page is closed.
type FailedPoller struct {
	System, User Dialer
	Interval     time.Duration // default 30 s
	Log          *slog.Logger
	// Observe receives "<scope>/<unit>" -> 1 for every failed unit. It is
	// not called when a bus could not be read, so an unreadable manager
	// does not look like its failures were fixed.
	Observe func(ctx context.Context, now time.Time, failed map[string]float64)
}

// Run polls until ctx ends.
func (p *FailedPoller) Run(ctx context.Context) {
	if p.Interval == 0 {
		p.Interval = 30 * time.Second
	}
	if p.Log == nil {
		p.Log = slog.Default()
	}
	conns := map[string]Bus{}
	defer func() {
		for _, b := range conns {
			_ = b.Close()
		}
	}()
	dialers := map[string]Dialer{ScopeSystem: p.System, ScopeUser: p.User}
	warned := map[string]bool{}
	tick := time.NewTicker(p.Interval)
	defer tick.Stop()
	for {
		failed := map[string]float64{}
		ok := true
		for scope, dial := range dialers {
			if dial == nil {
				continue
			}
			names, err := p.failed(ctx, conns, scope, dial)
			if err != nil {
				ok = false
				if !warned[scope] {
					p.Log.Warn("services: cannot read failed units", "scope", scope, "err", err)
					warned[scope] = true
				}
				continue
			}
			warned[scope] = false
			for _, n := range names {
				failed[scope+"/"+n] = 1
			}
		}
		if ok {
			p.Observe(ctx, time.Now(), failed)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (p *FailedPoller) failed(ctx context.Context, conns map[string]Bus, scope string, dial Dialer) ([]string, error) {
	bus := conns[scope]
	if bus == nil {
		var err error
		if bus, err = dial(ctx); err != nil {
			return nil, err
		}
		conns[scope] = bus
	}
	var units []listed
	if err := bus.Call(ctx, systemd, objPath, manager+".ListUnitsFiltered", []any{&units}, []string{"failed"}); err != nil {
		_ = bus.Close()
		delete(conns, scope) // reconnect next time
		return nil, err
	}
	names := make([]string, 0, len(units))
	for _, u := range units {
		names = append(names, u.Name)
	}
	return names, nil
}
