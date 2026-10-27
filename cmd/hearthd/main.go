// Command hearthd is Hearth's per-user daemon. It never runs as root
// (SECURITY S10) and serves the UI on 127.0.0.1 only (S1).
//
// Usage:
//
//	hearthd          run the daemon (normally via systemd --user)
//	hearthd open     open the UI in the browser (used by the launcher)
//	hearthd broker-call <method> [params-json]
//	                 call hearth-broker as this user and print the answer
//	                 (for testing the broker; no privilege of its own)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/alerts"
	"github.com/mohammed-w-mosabeh/hearth/internal/api"
	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/diskusage"
	"github.com/mohammed-w-mosabeh/hearth/internal/docker"
	"github.com/mohammed-w-mosabeh/hearth/internal/homepage"
	"github.com/mohammed-w-mosabeh/hearth/internal/launch"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/power"
	"github.com/mohammed-w-mosabeh/hearth/internal/procctl"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/services"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/internal/unitctl"
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

	// A soft heap limit inside the resource budget (CLAUDE.md: < 60 MB RSS
	// with the UI open): the GC works harder only when heavy work such as
	// measuring a large home pushes the heap up. GOMEMLIMIT overrides it.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(40 << 20)
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
	case (len(args) == 2 || len(args) == 3) && args[0] == "broker-call":
		err = brokerCall(ctx, args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: hearthd [open|version|broker-call <method> [params-json]]")
		os.Exit(2)
	}
	if err != nil {
		log.Error("hearthd failed", "err", err) // an attribute: JSON-escaped, so error text cannot forge log lines
		os.Exit(1)
	}
}

func refuseRoot(euid int) error {
	if euid == 0 {
		return errors.New("hearthd must not run as root; it runs as your user (systemctl --user start hearthd)")
	}
	return nil
}

// serverHook, set only by tests, sees the fully wired server.
var serverHook func(*api.Server)

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

	// Alerts: rules evaluated on every metrics sample (1 s with the UI
	// open, 15 s otherwise) and on failed units, notified on the desktop.
	notifier := alerts.DesktopNotifier{Dial: dbusx.SessionBus}
	engine := alerts.NewEngine(db, notifier, srv.Hub, log)
	if err := engine.Load(ctx); err != nil {
		return err
	}
	defer engine.Wait()
	(&alerts.API{Store: db, Engine: engine, Notify: notifier}).Register(srv.API)
	failed := &services.FailedPoller{
		System: func(ctx context.Context) (services.Bus, error) { return dbusx.SystemBus(ctx) },
		User:   func(ctx context.Context) (services.Bus, error) { return dbusx.SessionBus(ctx) },
		Log:    log,
		Observe: func(ctx context.Context, now time.Time, f map[string]float64) {
			engine.Observe(ctx, now, "unit.failed", f)
		},
	}
	failedCtx, stopFailed := context.WithCancel(ctx)
	failedDone := make(chan struct{})
	go func() { failed.Run(failedCtx); close(failedDone) }()
	defer func() { stopFailed(); <-failedDone }()

	ds, err := store.NewDownsampler(ctx, db, log, nil)
	if err != nil {
		return err
	}
	sampler := collect.New(collect.Config{
		Pub:   srv.Hub,
		Log:   log,
		Power: pw.Snapshot,
		// The sampler goroutine owns ds; it is flushed after the sampler stops.
		OnSample: func(s collect.Sample) {
			ds.Add(ctx, s.Time, s.Points())
			for metric, values := range alerts.FromSample(s) {
				engine.Observe(ctx, s.Time, metric, values)
			}
		},
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

	// Folder size explorer: the home and disks the user owns.
	sizes := diskusage.New(diskusage.Config{
		Roots: diskusage.UserRoots(appsCfg.Home, func() ([]string, error) { return collect.MountPoints("") }),
	})
	defer sizes.Close()
	srv.API.HandleFunc("GET /api/v1/disks/roots", sizes.HandleRoots)
	srv.API.HandleFunc("GET /api/v1/disks/usage", sizes.HandleUsage)

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
	unitctl.Register(reg, unitctl.Config{
		System: func(ctx context.Context) (unitctl.Bus, error) { return dbusx.SystemBus(ctx) },
		User:   func(ctx context.Context) (unitctl.Bus, error) { return dbusx.SessionBus(ctx) },
	})
	// Debian packages: dpkg's database read on demand (watched while the
	// Packages page follows "pkgs"), changes through PackageKit.
	dpkgDB := discovery.NewDpkg(discovery.DpkgConfig{Pub: srv.Hub, Log: log})
	pkgsCtx, stopPkgs := context.WithCancel(ctx)
	pkgsDone := make(chan struct{})
	go func() { dpkgDB.Run(pkgsCtx); close(pkgsDone) }()
	defer func() { stopPkgs(); <-pkgsDone }()
	srv.API.HandleFunc("GET /api/v1/pkgs", dpkgDB.HandleList)
	pkgs.Register(reg, pkgs.Config{
		PK:   &pkgs.Client{Dial: func(ctx context.Context) (pkgs.Bus, error) { return dbusx.SystemBus(ctx) }},
		Dpkg: dpkgDB,
	})
	// Flatpak, Snap, pip --user, npm -g, cargo and Go binaries: listed
	// from their files; removed by the manager's own uninstall as a
	// transient user unit (outside this sandbox), or through snapd.
	extraFind := discovery.ExtraConfig{Home: appsCfg.Home, Getenv: os.Getenv}
	srv.API.HandleFunc("GET /api/v1/pkgs/{manager}", discovery.HandleExtra(extraFind))
	pkgs.RegisterExtra(reg, pkgs.ExtraConfig{Find: extraFind, Run: &pkgs.UserRunner{
		Bus:    func(ctx context.Context) (pkgs.Bus, error) { return dbusx.SessionBus(ctx) },
		LogDir: filepath.Join(paths.DataDir, "runs"),
	}})
	brokerClient := &broker.Client{}
	docker.Register(reg, brokerClient)
	docker.RegisterImages(reg, brokerClient)
	docker.RegisterProjects(reg, brokerClient)
	docker.RegisterPrune(reg, brokerClient)
	srv.API.HandleFunc("POST /api/v1/actions/{kind}/{action}", reg.Handle)
	srv.API.HandleFunc("GET /api/v1/broker/status", brokerClient.HandleStatus)
	srv.API.HandleFunc("GET /api/v1/services/journal", services.HandleJournal(safeexec.Run, services.CanReadSystemJournal))

	// Docker through the broker: objects, live logs and stats, each
	// followed only while a Docker page asks for it.
	dockerObjs := discovery.NewDockerWatcher(discovery.DockerConfig{Broker: brokerClient, Pub: srv.Hub, Log: log})
	dockerLogs := docker.NewLogs(brokerClient, srv.Hub, log)
	dockerStats := docker.NewStats(brokerClient, srv.Hub, log, 0)
	dockerCtx, stopDocker := context.WithCancel(ctx)
	var dockerWG sync.WaitGroup
	for _, run := range []func(context.Context){dockerObjs.Run, dockerLogs.Run, dockerStats.Run} {
		dockerWG.Add(1)
		go func() { defer dockerWG.Done(); run(dockerCtx) }()
	}
	defer func() { stopDocker(); dockerWG.Wait() }()
	srv.API.HandleFunc("GET /api/v1/docker", dockerObjs.HandleSnapshot)
	// Container usage for Home and the "container exited" alert, from the
	// cgroups; the broker is asked only when the running set changes.
	dockerMon := &docker.Monitor{B: brokerClient, Pub: srv.Hub, Log: log,
		Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
			engine.Observe(ctx, now, "container.exited", v)
		}}
	dockerWG.Add(1)
	go func() { defer dockerWG.Done(); dockerMon.Run(dockerCtx) }()
	srv.API.HandleFunc("GET /api/v1/docker/usage", dockerMon.HandleUsage)
	srv.API.HandleFunc("GET /api/v1/docker/containers/{id}", docker.HandleInspect(brokerClient))
	images := &docker.Images{B: brokerClient, Registry: docker.NewRegistry(), Run: safeexec.Run, CacheDir: filepath.Join(paths.DataDir, "trivy")}
	srv.API.HandleFunc("GET /api/v1/docker/images/{id}", images.HandleDetail)
	srv.API.HandleFunc("GET /api/v1/docker/images/{id}/updates", images.HandleUpdates)
	srv.API.HandleFunc("POST /api/v1/docker/images/{id}/scan", images.HandleScan)
	srv.API.HandleFunc("GET /api/v1/docker/trivy", images.HandleTrivy)
	srv.API.HandleFunc("GET /api/v1/docker/cleanup", docker.HandleCleanup(brokerClient))
	srv.API.HandleFunc("GET /api/v1/docker/exec/{id}", docker.HandleExec(docker.ExecConfig{
		Open: func(ctx context.Context, method string, params any) (docker.ExecSession, error) {
			return brokerClient.Open(ctx, method, params)
		},
		Audit: db.Audit, UID: os.Getuid(), Log: log,
	}))

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

	// Homepage tiles; their URLs are checked only while Home is open.
	checker := homepage.NewChecker(db, srv.Hub, log)
	homepageAPI := &homepage.API{DB: db, Checker: checker}
	homepageAPI.Register(srv.API)
	// Suggestions are found only when the user asks for them.
	srv.API.HandleFunc("GET /api/v1/homepage/suggestions", homepageAPI.HandleSuggestions(&homepage.Suggester{
		Docker: brokerClient, SelfPort: srv.Port(), Probe: homepage.HTTPProbe(nil),
	}))
	checkCtx, stopCheck := context.WithCancel(ctx)
	checkDone := make(chan struct{})
	go func() { checker.Run(checkCtx); close(checkDone) }()
	defer func() { stopCheck(); <-checkDone }()

	if serverHook != nil {
		serverHook(srv) // every route is registered by now
	}
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

// brokerCall is `hearthd broker-call <method> [params-json]`.
func brokerCall(ctx context.Context, args []string) error {
	var params any
	if len(args) == 2 {
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(args[1]), &raw); err != nil {
			return fmt.Errorf("params must be JSON: %w", err)
		}
		params = raw
	}
	var out json.RawMessage
	if err := (&broker.Client{}).Call(ctx, args[0], params, &out); err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
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
