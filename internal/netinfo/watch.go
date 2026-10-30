package netinfo

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// ExposedWatcher reports listeners that newly listen on every address
// (0.0.0.0 or ::) to the alert engine ("net.new_listener"). A listener
// is known by protocol, port and owner; the known ones are kept (Load,
// Save) so a restart of hearthd does not report them again. The first
// run only records what is there. A new listener is reported once: the
// next poll leaves it out, which resolves the alert.
type ExposedWatcher struct {
	Resolver *Resolver
	Dump     Dumper
	Users    func() map[int]string
	// Load returns the known listeners; ok is false before the first run.
	Load    func(ctx context.Context) (known []string, ok bool, err error)
	Save    func(ctx context.Context, known []string) error
	Observe func(ctx context.Context, now time.Time, values map[string]float64)
	// Interval defaults to 60 s.
	Interval time.Duration
	Log      *slog.Logger
}

// maxKnown bounds the known set.
const maxKnown = 1000

// ephemeralUDP is where unconnected UDP sockets of clients live (Linux's
// default ip_local_port_range starts at 32768): not services.
const ephemeralUDP = 32768

// Exposed returns the listeners on every address, by label
// ("TCP 8080 (nginx)"), which is also the alert instance.
func Exposed(s Sockets) map[string]bool {
	out := map[string]bool{}
	for _, e := range s.Listeners {
		if e.Scope != ScopeAll || (e.Proto == "udp" && e.Port >= ephemeralUDP) {
			continue
		}
		who := e.User
		if e.Owner != nil {
			switch {
			case e.Owner.GroupName != "":
				who = e.Owner.GroupName
			case e.Owner.Name != "":
				who = e.Owner.Name
			}
		}
		out[fmt.Sprintf("%s %d (%s)", strings.ToUpper(e.Proto), e.Port, who)] = true
	}
	return out
}

// ListenerSet returns every listener by label ("TCP 631 (cupsd)") with
// its addresses, sorted ("127.0.0.1, ::1"), for the change timeline.
// Unconnected UDP sockets of clients are left out, as in Exposed.
func ListenerSet(s Sockets) map[string]string {
	addrs := map[string]map[string]bool{}
	for _, e := range s.Listeners {
		if e.Proto == "udp" && e.Port >= ephemeralUDP {
			continue
		}
		who := e.User
		if e.Owner != nil {
			switch {
			case e.Owner.GroupName != "":
				who = e.Owner.GroupName
			case e.Owner.Name != "":
				who = e.Owner.Name
			}
		}
		l := fmt.Sprintf("%s %d (%s)", strings.ToUpper(e.Proto), e.Port, who)
		if addrs[l] == nil {
			addrs[l] = map[string]bool{}
		}
		addrs[l][e.Address] = true
	}
	out := make(map[string]string, len(addrs))
	for l, set := range addrs {
		list := make([]string, 0, len(set))
		for a := range set {
			list = append(list, a)
		}
		sort.Strings(list)
		out[l] = strings.Join(list, ", ")
	}
	return out
}

// Run polls until ctx ends.
func (w *ExposedWatcher) Run(ctx context.Context) {
	if w.Interval == 0 {
		w.Interval = time.Minute
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	tick := time.NewTicker(w.Interval)
	defer tick.Stop()
	var known map[string]bool // nil until loaded
	for {
		known = w.poll(ctx, known, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// poll does one round and returns the known set.
func (w *ExposedWatcher) poll(ctx context.Context, known map[string]bool, now time.Time) map[string]bool {
	if known == nil {
		list, ok, err := w.Load(ctx)
		if err != nil {
			w.Log.Warn("net: known listeners", "err", err)
			return nil
		}
		if ok {
			known = map[string]bool{}
			for _, k := range list {
				known[k] = true
			}
		}
	}
	s, err := w.Resolver.Read(w.Dump, w.Users())
	if err != nil {
		w.Log.Debug("net: listeners", "err", err)
		return known
	}
	now2 := Exposed(s)
	if known == nil { // first run ever: what is there is known
		w.save(ctx, now2)
		w.Observe(ctx, now, map[string]float64{})
		return now2
	}
	fresh := map[string]float64{}
	for k := range now2 {
		if !known[k] {
			fresh[k] = 1
		}
	}
	w.Observe(ctx, now, fresh)
	if len(fresh) > 0 {
		for k := range fresh {
			known[k] = true
		}
		w.save(ctx, known)
	}
	return known
}

func (w *ExposedWatcher) save(ctx context.Context, known map[string]bool) {
	list := make([]string, 0, len(known))
	for k := range known {
		list = append(list, k)
	}
	sort.Strings(list)
	if len(list) > maxKnown {
		list = list[len(list)-maxKnown:]
	}
	if err := w.Save(ctx, list); err != nil {
		w.Log.Warn("net: save known listeners", "err", err)
	}
}
