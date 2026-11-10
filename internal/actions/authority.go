package actions

import "strings"

// Authority says whether running an action can ask for a password
// (SECURITY S15d). An automation runs a step unattended only when its
// authority is none; everything else waits for the user's approval.
type Authority string

const (
	// AuthorityNone: runs as the user; nothing in it can ask polkit.
	AuthorityNone Authority = "none"
	// AuthorityPolkit: may ask, through systemd, the broker, PackageKit,
	// snapd or a system service. The default for anything not listed.
	AuthorityPolkit Authority = "polkit"
)

// noPassword are the actions that run as the user and can never ask for
// a password, each checked against its code: the user's own processes,
// apps, audio, autostart entries and session settings; power profiles and
// brightness (non-interactive calls the active session is allowed);
// NetworkManager connection switches (made without interaction when
// unattended, S15d); Hearth's own listener, backup and the trash.
var noPassword = map[string]bool{
	"proc/terminate": true, "proc/kill": true, "proc/freeze": true, "proc/resume": true, "proc/renice": true, "proc/ionice": true,
	"app/launch": true, "app/close": true, "game/launch": true,
	"audio/volume": true, "audio/mute": true, "audio/default": true,
	"autostart/set": true, "autostart/add": true,
	"session/wallpaper": true, "session/wallpaper-uris": true, "session/dnd": true,
	"power/profile": true, "system/brightness": true,
	"network/connect": true, "network/disconnect": true, "wifi/scan": true, "wifi/radio": true,
	"proxy/listen": true, "proxy/route": true,
	"hearth/backup": true,
	"space/trash":   true, "space/empty-trash": true,
	"mode/arm": true, "mode/disarm": true, "mode/dismiss": true,
	"npm/remove": true, "cargo/remove": true, "gobin/remove": true,
}

// userScoped are actions whose target names a scope first: "user/..." runs
// through the user's own manager (or installation) and never asks;
// "system/..." may.
var userScoped = map[string]bool{
	"unit/start": true, "unit/stop": true, "unit/restart": true, "unit/enable": true, "unit/disable": true,
	"unit/mask": true, "unit/unmask": true, "unit/override": true, "unit/cap": true,
	"job/run": true, "job/pause": true, "job/resume": true,
	"flatpak/remove": true,
}

// AuthorityOf is the authority of running key on r: the action's own
// declaration when it has one, else the table above, else polkit.
func (r *Registry) AuthorityOf(key string, req Request) Authority {
	a, ok := r.Lookup(key)
	if !ok {
		return AuthorityPolkit
	}
	if a.AuthorityFor != nil {
		return a.AuthorityFor(req)
	}
	if a.Authority != "" {
		return a.Authority
	}
	return DefaultAuthority(key, req.Target)
}

// DefaultAuthority is the table's answer for key and target.
func DefaultAuthority(key, target string) Authority {
	if noPassword[key] {
		return AuthorityNone
	}
	if userScoped[key] && strings.HasPrefix(target, "user/") {
		return AuthorityNone
	}
	return AuthorityPolkit
}

// Classified lists the actions the table names, for a test that each is
// a registered action (a renamed one would silently become polkit).
func Classified() []string {
	out := make([]string, 0, len(noPassword)+len(userScoped))
	for k := range noPassword {
		out = append(out, k)
	}
	for k := range userScoped {
		out = append(out, k)
	}
	return out
}
