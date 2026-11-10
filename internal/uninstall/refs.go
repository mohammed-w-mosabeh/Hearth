package uninstall

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Hearth's own references to an item (docs/plan/uninstall.md §2.6): its
// favorites, alert rules on its units, a desktop game's playtime, and
// mode files that name it. Homepage tiles hold web addresses only, and
// usage history ages out by itself, so neither is touched.

// RefData is what Hearth holds now.
type RefData struct {
	Favorites []string // the apps.favorites setting's IDs
	Rules     []store.AlertRule
	Playtime  map[string]int64 // game_playtime, by game ID
	Modes     []modes.Mode
}

// Refs lists Hearth's references to res; units are the plan's units as
// "system/<name>" or "user/<name>". Mode references start unchecked:
// editing a mode file disarms its triggers (S15b).
func Refs(d RefData, res Resolved, units []string) []Candidate {
	ids := res.Identifiers
	unitNames := map[string]bool{}
	for _, u := range units {
		_, name, _ := strings.Cut(u, "/")
		unitNames[name] = true
	}
	ref := func(id, name, reason string, pre bool) Candidate {
		return Candidate{Group: GroupRefs, Kind: KindRef, ID: id, Name: name, Preselect: pre, Reason: reason}
	}
	var out []Candidate
	for _, f := range d.Favorites {
		if slices.Contains(ids.Desktop, f) {
			out = append(out, ref("favorite:"+f, f, "a favorite in Desktop apps", true))
		}
	}
	for _, r := range d.Rules {
		if r.Metric == "unit.failed" && unitNames[r.Target] {
			out = append(out, ref("alert:"+strconv.FormatInt(r.ID, 10), r.Name, "an alert rule on "+r.Target, true))
		}
	}
	for _, id := range ids.Desktop {
		if _, ok := d.Playtime["desktop."+id]; ok {
			out = append(out, ref("playtime:desktop."+id, id, "the playtime Hearth tracked", true))
		}
	}
	// A launcher's game, by its own ID (desktop games are covered above).
	if gid, ok := strings.CutPrefix(res.Item, "game:"); ok && !strings.HasPrefix(gid, "desktop.") {
		if _, has := d.Playtime[gid]; has {
			out = append(out, ref("playtime:"+gid, res.Owner.Name, "the playtime Hearth tracked", true))
		}
	}
	const warn = "editing the mode file disarms its triggers until you arm them again"
	for _, m := range d.Modes {
		for _, it := range m.Items {
			hit := false
			switch it.Kind {
			case modes.KindApp:
				hit = slices.Contains(ids.Desktop, it.Target)
			case modes.KindUnit:
				_, name, _ := strings.Cut(it.Target, "/")
				hit = unitNames[name]
			}
			if hit {
				out = append(out, ref(modeRef(m.ID, it.Kind, it.Target), m.Name+": "+it.Target,
					fmt.Sprintf("the mode %s %ss it; %s", m.Name, it.Do, warn), false))
			}
		}
		for _, a := range m.Session.Open {
			if slices.Contains(ids.Desktop, a) {
				out = append(out, ref(modeRef(m.ID, "open", a), m.Name+": "+a, "the mode "+m.Name+" opens it; "+warn, false))
			}
		}
	}
	return out
}

// modeRef is a mode reference's ID: "mode:<mode id>:<kind>:<target>".
func modeRef(mode, kind, target string) string { return "mode:" + mode + ":" + kind + ":" + target }
