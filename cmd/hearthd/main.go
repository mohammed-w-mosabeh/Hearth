// Command hearthd is Hearth's per-user daemon. It never runs as root
// (SECURITY S10) and serves the UI on 127.0.0.1 only (S1).
//
// Usage:
//
//	hearthd          run the daemon (normally via systemd --user)
//	hearthd open     open the UI in the browser (used by the launcher)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/api"
	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/launch"
	"github.com/mohammed-w-mosabeh/hearth/internal/power"
	"github.com/mohammed-w-mosabeh/hearth/internal/procctl"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/services"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := refuseRoot(os.Geteuid()); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
	paths, err := resolvePaths(os.Getenv)
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch args := os.Args[1:]; {
	case len(args) == 0:
		err = run(ctx, paths, log, openBrowser)
	case len(args) == 1 && args[0] == "open":
		err = openUI(ctx, paths)
	case len(args) == 1 && (args[0] == "version" || args[0] == "--version"):
		fmt.Println(version())
	default:
		fmt.Fprintln(os.Stderr, "usage: hearthd [open|version]")
		os.Exit(2)
	}
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func refuseRoot(euid int) error {
	if euid == 0 {
		return errors.New("hearthd must not run as root; it runs as your user (systemctl --user start hearthd)")
	}
	return nil
}

// run starts the daemon and blocks until ctx is cancelled.
func run(ctx context.Context, paths Paths, log *slog.Logger, open func(context.Context, string) error) error {
	cfg, firstRun, err := loadConfig(ctx, paths)
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, paths.DBFile())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	am, err := auth.NewManager(paths.TokenFile())
	if err != nil {
		return err
	}
	srv, err := api.New(api.Config{Port: cfg.Port, Auth: am, Static: web.Dist(), Logger: log})
	if err != nil {
		return err
	}
	if err := srv.Listen(ctx); err != nil {
		return err
	}

	pw := power.NewWatcher(func(ctx context.Context) (dbusx.Bus, error) { return dbusx.SystemBus(ctx) }, log)
	powerCtx, stopPower := context.WithCancel(ctx)
	powerDone := make(chan struct{})
	go func() { pw.Run(powerCtx); close(powerDone) }()
	defer func() { stopPower(); <-powerDone }()
	srv.API.HandleFunc("GET /api/v1/power", pw.HandleGet)
	srv.API.HandleFunc("POST /api/v1/power/profile", pw.HandleSetProfile(db))

	ds, err := store.NewDownsampler(ctx, db, log, nil)
	if err != nil {
		return err
	}
	sampler := collect.New(collect.Config{
		Pub:   srv.Hub,
		Log:   log,
		Power: pw.Snapshot,
		// The sampler goroutine owns ds; it is flushed after the sampler stops.
		OnSample: func(s collect.Sample) { ds.Add(ctx, s.Time, s.Points()) },
	})
	srv.API.HandleFunc("GET /api/v1/metrics/live", sampler.HandleLive)
	srv.API.HandleFunc("GET /api/v1/metrics/history", collect.HandleHistory(db))
	srv.RegisterSettings(db)
	samplerCtx, stopSampler := context.WithCancel(ctx)
	samplerDone := make(chan struct{})
	go func() { sampler.Run(samplerCtx); close(samplerDone) }()
	defer func() {
		stopSampler()
		<-samplerDone
		ds.Close(context.Background())
	}()

	// Installed applications, kept current with fsnotify.
	appsCfg := discovery.ConfigFromEnv(os.Getenv)
	appsCfg.Pub, appsCfg.Log = srv.Hub, log
	apps := discovery.NewIndex(appsCfg)
	apps.Scan() // before serving, so the first page load has the list
	appsCtx, stopApps := context.WithCancel(ctx)
	appsDone := make(chan struct{})
	go func() { apps.Run(appsCtx); close(appsDone) }()
	defer func() { stopApps(); <-appsDone }()
	srv.API.HandleFunc("GET /api/v1/apps", apps.HandleList)
	srv.API.HandleFunc("GET /api/v1/apps/icons", apps.HandleIcons)
	running := func() []string { return collect.RunningApps("", os.Getuid()) }
	srv.API.HandleFunc("GET /api/v1/apps/running", apps.HandleRunning(running))

	// Typed actions (ARCHITECTURE "Actions endpoint"), audited in db.
	reg := actions.NewRegistry(db)
	procctl.Register(reg, procctl.Config{AppGroups: func(id string) ([]string, string, bool) {
		a, ok := apps.App(id)
		if !ok {
			return nil, "", false
		}
		var groups []string
		for _, u := range apps.Running(running())[id] {
			groups = append(groups, "app:"+u)
		}
		return groups, a.Name, true
	}})
	launch.Register(reg, launch.Config{
		Apps: apps, Home: appsCfg.Home, Path: appsCfg.Path,
		Bus: func(ctx context.Context) (launch.Bus, error) { return dbusx.SessionBus(ctx) },
	})
	srv.API.HandleFunc("POST /api/v1/actions/{kind}/{action}", reg.Handle)

	// Process scanning runs only while a client follows the procs topic.
	procs := collect.NewProcSampler(collect.ProcConfig{Pub: srv.Hub, Log: log, AppName: apps.Name})
	procsCtx, stopProcs := context.WithCancel(ctx)
	procsDone := make(chan struct{})
	go func() { procs.Run(procsCtx); close(procsDone) }()
	defer func() { stopProcs(); <-procsDone }()

	// systemd units, followed over D-Bus only while the Services page is open.
	units := services.NewWatcher(services.Config{
		System: func(ctx context.Context) (services.Bus, error) { return dbusx.SystemBus(ctx) },
		User:   func(ctx context.Context) (services.Bus, error) { return dbusx.SessionBus(ctx) },
		Pub:    srv.Hub, Log: log, UID: os.Getuid(),
	})
	unitsCtx, stopUnits := context.WithCancel(ctx)
	unitsDone := make(chan struct{})
	go func() { units.Run(unitsCtx); close(unitsDone) }()
	defer func() { stopUnits(); <-unitsDone }()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve() }()
	log.Info("hearthd started", "version", version(), "port", srv.Port(), "first_run", firstRun)

	if firstRun {
		if u, err := launchURL(paths, srv.Port()); err != nil {
			log.Warn("cannot build launch URL", "err", err)
		} else if err := open(ctx, u); err != nil {
			log.Warn("could not open the browser; use the Hearth launcher", "err", err)
		}
	}

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("hearthd: shutdown: %w", err)
	}
	return <-errc
}

// openUI is `hearthd open`: check the daemon is up, then open a fresh
// single-use launch link.
func openUI(ctx context.Context, paths Paths) error {
	b, err := os.ReadFile(paths.ConfigFile())
	if err != nil {
		return fmt.Errorf("hearthd is not set up yet; start it with: systemctl --user start hearthd (%w)", err)
	}
	cfg, err := parseConfig(b)
	if err != nil {
		return err
	}
	if err := checkHealth(ctx, cfg.Port); err != nil {
		return fmt.Errorf("hearthd is not running; start it with: systemctl --user start hearthd (%w)", err)
	}
	u, err := launchURL(paths, cfg.Port)
	if err != nil {
		return err
	}
	return openBrowser(ctx, u)
}

func checkHealth(ctx context.Context, port int) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/api/v1/health", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: %s", resp.Status)
	}
	return nil
}

// launchURL builds the /auth link from the current single-use token.
func launchURL(paths Paths, port int) (string, error) {
	b, err := os.ReadFile(paths.TokenFile())
	if err != nil {
		return "", fmt.Errorf("hearthd: read token: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/auth?t=" + tok, nil
}

// openBrowser opens url for the user. Under systemd (INVOCATION_ID is set)
// the browser would inherit hearthd's sandbox, so the user manager starts it
// in its own transient unit instead.
func openBrowser(ctx context.Context, url string) error {
	var err error
	if os.Getenv("INVOCATION_ID") != "" {
		_, err = safeexec.Run(ctx, "systemd-run-xdg-open",
			"--user", "--collect", "--quiet", "--", "/usr/bin/xdg-open", url)
	} else {
		_, err = safeexec.Run(ctx, "xdg-open", url)
	}
	return err
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		v := bi.Main.Version
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				v += " (" + s.Value[:12] + ")"
			}
		}
		return v
	}
	return "unknown"
}
