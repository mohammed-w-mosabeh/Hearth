package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/access"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// The automations' sources of ROADMAP 12.4 that need the session, the
// system bus or the journal.

// idleSeconds asks GNOME's idle monitor, then logind's idle hint.
func idleSeconds(ctx context.Context) (float64, error) {
	if bus, err := dbusx.SessionBus(ctx); err == nil {
		defer func() { _ = bus.Close() }()
		var ms uint64
		if err := bus.Call(ctx, "org.gnome.Mutter.IdleMonitor", "/org/gnome/Mutter/IdleMonitor/Core",
			"org.gnome.Mutter.IdleMonitor.GetIdletime", []any{&ms}); err == nil {
			return float64(ms) / 1000, nil
		}
	}
	bus, err := dbusx.SystemBus(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = bus.Close() }()
	props, err := bus.GetAll(ctx, "org.freedesktop.login1", "/org/freedesktop/login1", "org.freedesktop.login1.Manager")
	if err != nil {
		return 0, err
	}
	idle, _ := props["IdleHint"].Value().(bool)
	since, _ := props["IdleSinceHint"].Value().(uint64)
	if !idle || since == 0 {
		return 0, nil
	}
	return time.Since(time.UnixMicro(int64(since))).Seconds(), nil // #nosec G115 -- microseconds since 1970
}

// resumes delivers a value each time logind says the machine woke.
func resumes(ctx context.Context) (<-chan struct{}, error) {
	bus, err := dbusx.SystemBus(ctx)
	if err != nil {
		return nil, err
	}
	sigs, err := bus.Watch(ctx, dbus.WithMatchObjectPath("/org/freedesktop/login1"),
		dbus.WithMatchInterface("org.freedesktop.login1.Manager"), dbus.WithMatchMember("PrepareForSleep"))
	if err != nil {
		_ = bus.Close()
		return nil, err
	}
	out := make(chan struct{}, 1)
	go func() {
		defer func() { _ = bus.Close() }()
		defer close(out)
		for s := range sigs {
			if len(s.Body) == 1 {
				if going, ok := s.Body[0].(bool); ok && !going {
					select {
					case out <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return out, nil
}

// sshFailures counts the failed SSH logins since a time (S16j).
func sshFailures(ctx context.Context, since time.Time) (int, []string, error) {
	res, err := safeexec.Run(ctx, "journalctl-ssh", safeexec.SSHArgs(since.Unix())...)
	if err != nil && res.ExitCode != 1 {
		return 0, nil, err
	}
	n, sources := access.Failures(res.Stdout)
	return n, sources, nil
}

// securityUpdates counts the security updates PackageKit lists (cached
// until apt's lists change).
func securityUpdates(u *pkgs.Updates) func(context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		list, err := u.List(ctx)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, x := range list {
			if x.Security {
				n++
			}
		}
		return n, nil
	}
}

// imageUpdates are the managed apps whose last update preview found a
// newer image (S21a: only the user's own previews ask registries).
func imageUpdates(db *store.DB) func(context.Context) (map[string]bool, error) {
	return func(ctx context.Context) (map[string]bool, error) {
		raw, ok, err := db.Setting(ctx, stacks.SettingUpdates)
		if err != nil {
			return nil, err
		}
		out := map[string]bool{}
		if !ok {
			return out, nil
		}
		var notes map[string]stacks.UpdateNote
		if err := json.Unmarshal(raw, &notes); err != nil {
			return nil, errors.New("update notes could not be read")
		}
		for name, n := range notes {
			out[name] = len(n.Services) > 0
		}
		return out, nil
	}
}
