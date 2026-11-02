package services

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// listTimeout bounds one answer of HandleList.
const listTimeout = 5 * time.Second

// HandleList serves GET /api/v1/services: the units loaded now, one read of
// each manager. Unit files are left out, since listing them takes over a
// second and the live list on the services topic has them.
func (w *Watcher) HandleList(rw http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	msg := ListMsg{Kind: "list", Units: []Unit{}}
	for _, m := range []struct {
		scope string
		dial  Dialer
	}{{ScopeSystem, w.cfg.System}, {ScopeUser, w.cfg.User}} {
		units, reason := w.readOnce(ctx, m.scope, m.dial)
		msg.Units = append(msg.Units, units...)
		if reason != "" {
			if msg.Errors == nil {
				msg.Errors = map[string]string{}
			}
			msg.Errors[m.scope] = reason
		}
	}
	sort.SliceStable(msg.Units, func(i, j int) bool { return msg.Units[i].Name < msg.Units[j].Name })
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(rw).Encode(msg)
}

func (w *Watcher) readOnce(ctx context.Context, scope string, dial Dialer) ([]Unit, string) {
	if dial == nil {
		return nil, "not configured"
	}
	bus, err := dial(ctx)
	if err != nil {
		w.cfg.Log.Warn("services: connect", "scope", scope, "err", err)
		return nil, "cannot connect to the " + scope + " manager"
	}
	defer func() { _ = bus.Close() }()
	loaded, err := listUnits(ctx, bus)
	if err != nil {
		w.cfg.Log.Warn("services: units", "scope", scope, "err", err)
		return nil, "cannot list the " + scope + " units"
	}
	return merge(scope, loaded, nil), ""
}
