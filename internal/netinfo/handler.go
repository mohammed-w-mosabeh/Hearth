package netinfo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

// Config configures the network API.
type Config struct {
	Resolver *Resolver
	Dump     Dumper                                   // Dump in production
	Users    func() map[int]string                    // collect.Users in production
	Bus      func(context.Context) (dbusx.Bus, error) // the system bus
	Session  func(context.Context) (dbusx.Bus, error) // the user's bus (user units)
	Packages Packages                                 // dpkg owners (lineage)
	Paths    Paths
	Firewall FirewallConfig
	Log      *slog.Logger
}

// State is everything about the network but its sockets.
type State struct {
	Addressing
	NM        NM         `json:"nm"`
	Firewalls []Firewall `json:"firewalls"`
}

// HandleSockets serves GET /api/v1/net/sockets.
func (c Config) HandleSockets(w http.ResponseWriter, _ *http.Request) {
	s, err := c.Resolver.Read(c.Dump, c.Users())
	if err != nil {
		c.Log.Warn("net: sockets", "err", err)
		http.Error(w, "the sockets could not be read", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s)
}

// HandleState serves GET /api/v1/net/state.
func (c Config) HandleState(w http.ResponseWriter, r *http.Request) {
	st := State{Addressing: ReadAddressing(c.Paths), NM: NM{Connections: []NMConn{}}}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var bus dbusx.Bus
	if b, err := c.Bus(ctx); err != nil {
		c.Log.Debug("net: system bus", "err", err)
	} else {
		bus = b
		if cl, ok := b.(io.Closer); ok {
			defer func() { _ = cl.Close() }()
		}
		st.NM = ReadNM(ctx, bus)
	}
	st.Firewalls = ReadFirewalls(ctx, bus, c.Firewall)
	writeJSON(w, st)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
