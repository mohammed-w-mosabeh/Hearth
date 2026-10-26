// Package services lists systemd units, system and user (SPEC 10), from
// the system manager and the user manager over D-Bus, and keeps the list
// current from their signals. It runs only while a client follows the
// services topic, so an idle hearthd holds no bus connections for it.
package services

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

const (
	systemd = "org.freedesktop.systemd1"
	manager = "org.freedesktop.systemd1.Manager"
	objPath = dbus.ObjectPath("/org/freedesktop/systemd1")
)

// Scopes of a unit.
const (
	ScopeSystem = "system"
	ScopeUser   = "user"
)

// Unit is one unit as the UI lists it.
type Unit struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Description string `json:"description,omitempty"`
	Load        string `json:"load"`   // loaded, not-found, masked, bad-setting, error, or "" when not loaded
	Active      string `json:"active"` // active, inactive, failed, activating, deactivating, reloading
	Sub         string `json:"sub"`    // running, exited, dead, listening, waiting...
	// Enabled is the unit file state: enabled, disabled, static, masked,
	// indirect, generated, transient, alias...; "" when there is no file.
	Enabled  string `json:"enabled,omitempty"`
	Job      string `json:"job,omitempty"` // pending job type (start, stop...)
	Critical bool   `json:"critical,omitempty"`
}

// listed is ListUnits' a(ssssssouso).
type listed struct {
	Name, Description, Load, Active, Sub, Following string
	Path                                            dbus.ObjectPath
	JobID                                           uint32
	JobType                                         string
	JobPath                                         dbus.ObjectPath
}

// unitFile is ListUnitFiles' a(ss).
type unitFile struct {
	Path, State string
}

// listUnits returns the loaded units of the manager on bus.
func listUnits(ctx context.Context, bus dbusx.Bus) ([]listed, error) {
	var out []listed
	err := bus.Call(ctx, systemd, objPath, manager+".ListUnits", []any{&out})
	return out, err
}

// listFiles returns unit file states by unit name.
func listFiles(ctx context.Context, bus dbusx.Bus) (map[string]string, error) {
	var files []unitFile
	if err := bus.Call(ctx, systemd, objPath, manager+".ListUnitFiles", []any{&files}); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(files))
	for _, f := range files {
		name := filepath.Base(f.Path)
		if _, dup := out[name]; !dup { // the first path wins, as in systemd's search order
			out[name] = f.State
		}
	}
	return out, nil
}

// merge combines loaded units and unit files: every loaded unit, plus
// installed units that are not loaded (shown inactive). Left out, as
// systemctl list-units does without --all: units that are only referenced
// (not-found) and not running or failed, and template files
// (foo@.service), which cannot run themselves.
func merge(scope string, loaded []listed, files map[string]string) []Unit {
	out := make([]Unit, 0, len(loaded)+len(files)/2)
	seen := make(map[string]bool, len(loaded))
	for _, l := range loaded {
		seen[l.Name] = true
		if l.Load == "not-found" && l.Active == "inactive" {
			continue
		}
		u := Unit{Name: l.Name, Scope: scope, Description: l.Description, Load: l.Load, Active: l.Active,
			Sub: l.Sub, Enabled: files[l.Name], Job: l.JobType}
		if u.Enabled == "" && l.Load == "masked" {
			u.Enabled = "masked"
		}
		u.Critical = Critical(scope, l.Name)
		out = append(out, u)
	}
	for name, state := range files {
		if seen[name] || strings.Contains(name, "@.") {
			continue
		}
		out = append(out, Unit{Name: name, Scope: scope, Active: "inactive", Sub: "dead", Enabled: state,
			Critical: Critical(scope, name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// displayManagers are the units that run the login screen.
var displayManagers = map[string]bool{
	"display-manager.service": true, "gdm.service": true, "gdm3.service": true, "sddm.service": true,
	"lightdm.service": true, "lxdm.service": true, "xdm.service": true, "slim.service": true,
	"greetd.service": true, "cosmic-greeter.service": true,
}

// Critical reports whether stopping or disabling the unit can break the
// machine or the session (SPEC 10; SECURITY S16 asks for a double
// confirmation for these).
func Critical(scope, name string) bool {
	base := name
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		base = name[:i]
	}
	if at := strings.IndexByte(base, '@'); at > 0 {
		base = base[:at]
	}
	switch {
	case strings.HasPrefix(name, "hearth"):
		return true
	case strings.HasSuffix(name, ".slice") || strings.HasSuffix(name, ".target"):
		return true // stopping one stops everything in or wanting it
	case base == "dbus" || base == "dbus-broker":
		return true
	}
	if scope == ScopeUser {
		// The graphical session and what it cannot live without.
		return strings.HasPrefix(base, "gnome-session") || strings.HasPrefix(base, "org.gnome.Shell") ||
			strings.HasPrefix(base, "plasma-") || base == "pipewire" || base == "wireplumber" ||
			base == "xdg-desktop-portal" || base == "at-spi-dbus-bus"
	}
	return strings.HasPrefix(base, "systemd-") || base == "NetworkManager" || base == "polkit" ||
		base == "user" || base == "user-runtime-dir" || base == "getty" || base == "serial-getty" ||
		base == "-" || displayManagers[name] || name == "init.scope" || critMount(name)
}

// critMount: mounts the system cannot run without.
func critMount(name string) bool {
	switch name {
	case "-.mount", "boot.mount", "boot-efi.mount", "usr.mount", "var.mount", "home.mount", "tmp.mount", "run-user.mount":
		return true
	}
	return false
}
