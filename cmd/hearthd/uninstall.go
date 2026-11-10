package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/launcher"
	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall"
	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/archive"
)

// uninstallDeps are what Uninstall completely is built from.
type uninstallDeps struct {
	DB      *store.DB
	Apps    *discovery.Index
	Tools   *discovery.Tools
	Games   *discovery.Games
	Dpkg    *discovery.Dpkg
	Extra   discovery.ExtraConfig
	UserRun *pkgs.UserRunner
	PK      *pkgs.Client
	Broker  *broker.Client
	Modes   *modes.Set
	Home    string
	Log     *slog.Logger
}

type mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

func osRelease() string {
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(b))
}

// setupUninstall registers uninstall/remove and its routes (ROADMAP 11.7,
// SECURITY S16k) and returns the service, whose Run prunes old records.
func setupUninstall(api mux, reg *actions.Registry, d uninstallDeps) *uninstall.Service {
	uid := uint32(os.Getuid()) // #nosec G115 -- a uid
	procs := collect.NewProcSampler(collect.ProcConfig{})
	extra := func(ctx context.Context, m string) discovery.ExtraList {
		l, _ := discovery.ListExtra(ctx, d.Extra, m)
		return l
	}
	local := uninstall.Local{Home: d.Home, UID: uid, InstallDir: d.Games.InstallDir,
		SteamLibraries: func() []string { return discovery.SteamLibraries(d.Home) }}
	files := func(id string) []string { return archive.PackageFiles(archive.Roots{}, id) }
	b := &uninstall.Builder{
		Resolver: &uninstall.Resolver{Apps: d.Apps, Tools: d.Tools, Games: d.Games, Packages: d.Dpkg, Extra: extra, Home: d.Home},
		Local:    local,
		Running: uninstall.Running{Local: local, Self: os.Getpid(), Procs: procs.LookupGroups, PackageFiles: files,
			EtcUnits: broker.EtcUnits, UnitState: unitState},
		Scan: uninstall.Scan{Home: d.Home, UID: uid, Owned: d.Dpkg.Owners},
		PK:   d.PK,
		Simulate: func(ctx context.Context, names ...string) (string, error) {
			res, err := safeexec.Run(ctx, "apt-get-simulate", safeexec.AptSimulate(names...)...)
			if err != nil {
				return "", err
			}
			if res.ExitCode != 0 {
				return string(res.Stdout), &simError{strings.TrimSpace(string(res.Stderr))}
			}
			return string(res.Stdout), nil
		},
		Installed: d.Dpkg.Packages,
		Release:   osRelease,
		Session: func() []string {
			return uninstall.SessionPrograms("/proc", os.Getuid(), "/etc/systemd/system/display-manager.service")
		},
		Apps:  d.Apps.Apps,
		Extra: extra,
		Refs: func(ctx context.Context) (uninstall.RefData, error) {
			var rd uninstall.RefData
			if raw, ok, err := d.DB.Setting(ctx, "apps.favorites"); err == nil && ok {
				var f struct {
					IDs []string `json:"ids"`
				}
				if json.Unmarshal(raw, &f) == nil {
					rd.Favorites = f.IDs
				}
			}
			var err error
			if rd.Rules, err = d.DB.AlertRules(ctx); err != nil {
				return rd, err
			}
			if rd.Playtime, err = d.DB.Playtimes(ctx); err != nil {
				return rd, err
			}
			if d.Modes != nil {
				rd.Modes = d.Modes.Modes()
			}
			return rd, nil
		},
	}
	svc := &uninstall.Service{Builder: b, DB: d.DB, Log: d.Log}
	svc.Engine = &uninstall.Engine{
		Save:   svc.Save,
		Broker: d.Broker,
		PK:     d.PK,
		Extra: func(ctx context.Context, kind, id string) error {
			return pkgs.RemoveExtra(ctx, pkgs.ExtraConfig{Find: d.Extra, Run: d.UserRun}, kind, id)
		},
		Signal: func(pid int, start uint64, sig syscall.Signal) error {
			if got, err := procs.StartTime(pid); err != nil || got != start {
				return nil // gone, or another process now
			}
			return syscall.Kill(pid, sig)
		},
		Alive: func(pid int, start uint64) bool {
			got, err := procs.StartTime(pid)
			return err == nil && got == start
		},
		UserUnit: stopUserUnit,
		Trash:    uninstall.Trasher{Home: d.Home, Run: d.UserRun.Run},
		Open:     func(ctx context.Context, url string) error { return launcher.Open(ctx, launcherConfig(), url) },
		Refs:     uninstall.DBRefs{DB: d.DB, Modes: d.Modes},
		Audit: func(ctx context.Context, action, target, result string) {
			_ = d.DB.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(), Action: action, Target: target, Result: result})
		},
	}
	svc.Register(reg)
	api.HandleFunc("GET /api/v1/uninstall/plan", svc.HandlePlan)
	api.HandleFunc("GET /api/v1/uninstall/ops", svc.HandleOps)
	return svc
}

type simError struct{ msg string }

func (e *simError) Error() string { return "apt-get: " + e.msg }

const (
	sdDest    = "org.freedesktop.systemd1"
	sdPath    = dbus.ObjectPath("/org/freedesktop/systemd1")
	sdManager = "org.freedesktop.systemd1.Manager"
	sdUnit    = "org.freedesktop.systemd1.Unit"
)

// unitState reads a unit's active state and enablement from the system or
// user manager.
func unitState(user bool, name string) (string, string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var bus *dbusx.Conn
	var err error
	if user {
		bus, err = dbusx.SessionBus(ctx)
	} else {
		bus, err = dbusx.SystemBus(ctx)
	}
	if err != nil {
		return "", "", false
	}
	defer func() { _ = bus.Close() }()
	var path dbus.ObjectPath
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".LoadUnit", []any{&path}, name); err != nil {
		return "", "", false
	}
	props, err := bus.GetAll(ctx, sdDest, path, sdUnit)
	if err != nil {
		return "", "", false
	}
	active, _ := props["ActiveState"].Value().(string)
	enabled, _ := props["UnitFileState"].Value().(string)
	load, _ := props["LoadState"].Value().(string)
	return active, enabled, load == "loaded"
}

// stopUserUnit stops and disables one of the user's own units.
func stopUserUnit(ctx context.Context, name string) error {
	if err := safeexec.UnitName(name); err != nil {
		return err
	}
	bus, err := dbusx.SessionBus(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = bus.Close() }()
	var job dbus.ObjectPath
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".StopUnit", []any{&job}, name, "replace"); err != nil {
		return err
	}
	var changes []struct{ Type, File, Dest string }
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".DisableUnitFiles", []any{&changes}, []string{name}, false); err != nil {
		return err
	}
	return bus.Call(ctx, sdDest, sdPath, sdManager+".Reload", nil)
}
