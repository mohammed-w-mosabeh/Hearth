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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/access"
	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/alerts"
	"github.com/mohammed-w-mosabeh/hearth/internal/api"
	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
	"github.com/mohammed-w-mosabeh/hearth/internal/backup"
	"github.com/mohammed-w-mosabeh/hearth/internal/bootinfo"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/changes"
	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/connectivity"
	"github.com/mohammed-w-mosabeh/hearth/internal/crashes"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/diskusage"
	"github.com/mohammed-w-mosabeh/hearth/internal/docker"
	"github.com/mohammed-w-mosabeh/hearth/internal/dropin"
	"github.com/mohammed-w-mosabeh/hearth/internal/firewall"
	"github.com/mohammed-w-mosabeh/hearth/internal/forecast"
	"github.com/mohammed-w-mosabeh/hearth/internal/hardware"
	"github.com/mohammed-w-mosabeh/hearth/internal/homepage"
	"github.com/mohammed-w-mosabeh/hearth/internal/insight"
	"github.com/mohammed-w-mosabeh/hearth/internal/integrity"
	"github.com/mohammed-w-mosabeh/hearth/internal/launch"
	"github.com/mohammed-w-mosabeh/hearth/internal/launcher"
	"github.com/mohammed-w-mosabeh/hearth/internal/logview"
	"github.com/mohammed-w-mosabeh/hearth/internal/lynis"
	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/netinfo"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/posture"
	"github.com/mohammed-w-mosabeh/hearth/internal/power"
	"github.com/mohammed-w-mosabeh/hearth/internal/procctl"
	"github.com/mohammed-w-mosabeh/hearth/internal/profiles"
	"github.com/mohammed-w-mosabeh/hearth/internal/proxy"
	"github.com/mohammed-w-mosabeh/hearth/internal/reports"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/scheduler"
	"github.com/mohammed-w-mosabeh/hearth/internal/scheduler/schedapi"
	"github.com/mohammed-w-mosabeh/hearth/internal/services"
	"github.com/mohammed-w-mosabeh/hearth/internal/session"
	"github.com/mohammed-w-mosabeh/hearth/internal/settings"
	"github.com/mohammed-w-mosabeh/hearth/internal/smart"
	"github.com/mohammed-w-mosabeh/hearth/internal/space"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/importer"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/internal/terminal"
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

// registryHook, set only by tests, sees every registered action.
var registryHook func(*actions.Registry)

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
	readOnly, skippedTokens, err := auth.NewTokens(paths.TokensDir())
	if err != nil {
		return err
	}
	if len(skippedTokens) > 0 {
		log.Warn("tokens: ignored files that are not private token files", "files", skippedTokens)
	}
	srv, err := api.New(api.Config{Port: cfg.Port, Auth: am, Tokens: readOnly, Static: web.Dist(), Logger: log, Version: version()})
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
	channels := &alerts.Channels{Path: filepath.Join(paths.ConfigDir, "channels.json"), Log: log}
	if err := channels.Load(); err != nil {
		log.Warn("alerts: remote channels are off", "err", err)
	}
	engine := alerts.NewEngine(db, alerts.Fanout{notifier, channels}, srv.Hub, log)
	if err := engine.Load(ctx); err != nil {
		return err
	}
	defer engine.Wait()
	(&alerts.API{Store: db, Engine: engine, Notify: notifier, Channels: channels}).Register(srv.API)
	logWatch := &alerts.LogWatcher{Read: services.JournalTail(safeexec.Run), Rules: db.AlertRules, Log: log,
		Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
			engine.Observe(ctx, now, alerts.LogMetric, v)
		}}
	logCtx, stopLog := context.WithCancel(ctx)
	logDone := make(chan struct{})
	go func() { logWatch.Run(logCtx); close(logDone) }()
	defer func() { stopLog(); <-logDone }()
	// Failed hearth-job units are the Scheduler's ("job.failed", for jobs
	// that notify), not "unit.failed"; the job stores are set further down.
	var jobStores atomic.Pointer[map[string]*scheduler.Store]
	failed := &services.FailedPoller{
		System: func(ctx context.Context) (services.Bus, error) { return dbusx.SystemBus(ctx) },
		User:   func(ctx context.Context) (services.Bus, error) { return dbusx.SessionBus(ctx) },
		Log:    log,
		Observe: func(ctx context.Context, now time.Time, f map[string]float64) {
			if st := jobStores.Load(); st != nil {
				units, failedJobs := scheduler.SplitFailures(f, *st)
				engine.Observe(ctx, now, "unit.failed", units)
				engine.Observe(ctx, now, "job.failed", failedJobs)
				return
			}
			engine.Observe(ctx, now, "unit.failed", f)
		},
	}
	failedCtx, stopFailed := context.WithCancel(ctx)
	failedDone := make(chan struct{})
	go func() { failed.Run(failedCtx); close(failedDone) }()
	defer func() { stopFailed(); <-failedDone }()

	connWatch := connectivity.New(connectivity.Options{Store: db, Log: log, Observe: engine.Observe,
		Gateways: func() []string {
			var out []string
			for _, r := range netinfo.ReadAddressing(netinfo.Paths{}).Gateways {
				out = append(out, r.Gateway)
			}
			return out
		}})
	connWatch.Register(srv.API)
	connCtx, stopConn := context.WithCancel(ctx)
	connDone := make(chan struct{})
	go func() { connWatch.Run(connCtx); close(connDone) }()
	defer func() { stopConn(); <-connDone }()
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
			for _, n := range s.Net {
				if !n.Virtual {
					connWatch.Count(s.Time, n.Name, n.RxBytes, n.TxBytes)
				}
			}
			ds.Add(ctx, s.Time, append(s.Points(), connWatch.Points()...))
			for metric, values := range alerts.FromSample(s) {
				engine.Observe(ctx, s.Time, metric, values)
			}
		},
	})
	srv.API.HandleFunc("GET /api/v1/metrics/live", sampler.HandleLive)
	srv.API.HandleFunc("GET /api/v1/metrics/history", collect.HandleHistory(db))
	srv.RegisterSettings(db)
	srv.RegisterTokens(db, func(ctx context.Context, action, target, result string) error {
		return db.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(), Action: action, Target: target, Result: result})
	})
	srv.SetMetrics(func(ctx context.Context) ([]byte, bool) {
		if !api.MetricsEnabled(ctx, db) {
			return nil, false
		}
		firing := 0
		if open, err := db.OpenAlertEvents(ctx); err == nil {
			firing = len(open)
		}
		s, _ := sampler.Ring().Last() // an empty sample before the first tick
		return collect.OpenMetrics(s, firing), true
	})
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

	// Games: Steam, Lutris and Heroic libraries read on demand, and the
	// time desktop games run sampled once a minute.
	games := discovery.NewGames(discovery.GamesConfig{Home: appsCfg.Home, Apps: apps, Running: running, Store: db, Log: log})
	gamesCtx, stopGames := context.WithCancel(ctx)
	gamesDone := make(chan struct{})
	go func() { games.Track(gamesCtx, time.Minute); close(gamesDone) }()
	defer func() { stopGames(); <-gamesDone }()
	srv.API.HandleFunc("GET /api/v1/games", games.HandleList)
	srv.API.HandleFunc("GET /api/v1/games/covers", games.HandleCovers)

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
		Apps: apps, Games: games, Home: appsCfg.Home, Path: appsCfg.Path,
		Bus: func(ctx context.Context) (launch.Bus, error) { return dbusx.SessionBus(ctx) },
	})
	// Settings page: D-Bus services, PipeWire's tools, autostart entries.
	autostart := discovery.NewAutostart(discovery.AutostartConfig{
		ConfigHome: os.Getenv("XDG_CONFIG_HOME"), ConfigDirs: splitList(os.Getenv("XDG_CONFIG_DIRS")),
		Desktops: appsCfg.Desktops, Apps: apps,
	}, appsCfg.Home)
	sysSettings := settings.New(settings.Config{
		System:    func(ctx context.Context) (settings.Bus, error) { return dbusx.SystemBus(ctx) },
		Audio:     safeexec.Run,
		Autostart: autostart,
	})
	sysSettings.Register(reg)
	// Session settings a mode changes (ROADMAP 10.3): wallpaper and Do Not
	// Disturb on GNOME, other desktops detected and refused by name; and the
	// power profile as an action.
	sess := session.New(session.Config{Desktop: session.DetectDesktop(os.Getenv("XDG_CURRENT_DESKTOP")), Home: appsCfg.Home})
	sess.Register(reg)
	pw.Register(reg)
	srv.API.HandleFunc("GET /api/v1/system/info", sysSettings.HandleInfo)
	srv.API.HandleFunc("GET /api/v1/system/timezones", sysSettings.HandleTimezones)
	srv.API.HandleFunc("GET /api/v1/system/network", sysSettings.HandleNetwork)
	srv.API.HandleFunc("GET /api/v1/system/audio", sysSettings.HandleAudio)
	srv.API.HandleFunc("GET /api/v1/system/autostart", sysSettings.HandleAutostart)
	unitCfg := unitctl.Config{
		System:    func(ctx context.Context) (unitctl.Bus, error) { return dbusx.SystemBus(ctx) },
		User:      func(ctx context.Context) (unitctl.Bus, error) { return dbusx.SessionBus(ctx) },
		UserDir:   filepath.Join(filepath.Dir(paths.ConfigDir), "systemd/user"),
		SystemDir: dropin.SystemDir,
		Broker:    &broker.Client{},
	}
	unitctl.Register(reg, unitCfg)
	srv.API.HandleFunc("GET /api/v1/services/{scope}/{name}/override", unitctl.HandleOverride(unitCfg))
	// Debian packages: dpkg's database read on demand (watched while the
	// Packages page follows "pkgs"), changes through PackageKit.
	dpkgDB := discovery.NewDpkg(discovery.DpkgConfig{Pub: srv.Hub, Log: log})
	pkgsCtx, stopPkgs := context.WithCancel(ctx)
	pkgsDone := make(chan struct{})
	go func() { dpkgDB.Run(pkgsCtx); close(pkgsDone) }()
	defer func() { stopPkgs(); <-pkgsDone }()
	srv.API.HandleFunc("GET /api/v1/pkgs", dpkgDB.HandleList)
	srv.API.HandleFunc("GET /api/v1/pkgs/apt/{id}", dpkgDB.HandleDetail)
	pkgCfg := pkgs.Config{
		PK:   &pkgs.Client{Dial: func(ctx context.Context) (pkgs.Bus, error) { return dbusx.SystemBus(ctx) }},
		Dpkg: dpkgDB,
	}
	pkgs.Register(reg, pkgCfg)
	// Updates are cached until dpkg's database or apt's lists change.
	updates := &pkgs.Updates{PK: pkgCfg.PK, Stamp: func() string {
		return discovery.FileStamp("/var/lib/dpkg/status", "/var/lib/apt/lists")
	}}
	srv.API.HandleFunc("GET /api/v1/pkgs/updates", updates.HandleList)
	// Flatpak, Snap, pip --user, npm -g, cargo and Go binaries: listed
	// from their files; removed by the manager's own uninstall as a
	// transient user unit (outside this sandbox), or through snapd.
	extraFind := discovery.ExtraConfig{Home: appsCfg.Home, Getenv: os.Getenv}
	srv.API.HandleFunc("GET /api/v1/pkgs/{manager}", discovery.HandleExtra(extraFind))
	// Tools: every program in PATH with its owner, read from files only.
	tools := discovery.NewTools(discovery.ToolsConfig{Extra: extraFind, Dpkg: dpkgDB,
		Path: filepath.SplitList(os.Getenv("PATH")), Run: safeexec.Run, Log: log})
	srv.API.HandleFunc("GET /api/v1/tools", tools.HandleList)
	srv.API.HandleFunc("GET /api/v1/tools/man", tools.HandleMan)
	// Network: sockets from sock_diag, owners from cgroups and the user's
	// own /proc/<pid>/fd; NetworkManager and firewall units over D-Bus.
	netAPI := netinfo.Config{Resolver: &netinfo.Resolver{AppName: apps.Name}, Dump: netinfo.Dump, Users: collect.Users,
		Bus:      func(ctx context.Context) (dbusx.Bus, error) { return dbusx.SystemBus(ctx) },
		Session:  func(ctx context.Context) (dbusx.Bus, error) { return dbusx.SessionBus(ctx) },
		Packages: dpkgDB, Log: log}
	srv.API.HandleFunc("GET /api/v1/net/sockets", netAPI.HandleSockets)
	srv.API.HandleFunc("GET /api/v1/net/state", netAPI.HandleState)
	srv.API.HandleFunc("GET /api/v1/net/lineage", netAPI.HandleLineage)
	// A new listener on every address raises "net.new_listener" once.
	const knownListeners = "net.known_listeners"
	exposed := &netinfo.ExposedWatcher{Resolver: &netinfo.Resolver{AppName: apps.Name}, Dump: netinfo.Dump, Users: collect.Users, Log: log,
		Load: func(ctx context.Context) ([]string, bool, error) {
			raw, ok, err := db.Setting(ctx, knownListeners)
			if err != nil || !ok {
				return nil, ok, err
			}
			var list []string
			return list, true, json.Unmarshal(raw, &list)
		},
		Save: func(ctx context.Context, known []string) error {
			b, err := json.Marshal(known)
			if err != nil {
				return err
			}
			return db.SetSetting(ctx, knownListeners, b)
		},
		Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
			engine.Observe(ctx, now, "net.new_listener", v)
		},
	}
	exposedCtx, stopExposed := context.WithCancel(ctx)
	exposedDone := make(chan struct{})
	go func() { exposed.Run(exposedCtx); close(exposedDone) }()
	defer func() { stopExposed(); <-exposedDone }()
	userRun := &pkgs.UserRunner{
		Bus:    func(ctx context.Context) (pkgs.Bus, error) { return dbusx.SessionBus(ctx) },
		LogDir: filepath.Join(paths.DataDir, "runs"),
	}
	pkgs.RegisterExtra(reg, pkgs.ExtraConfig{Find: extraFind, Run: userRun})
	brokerClient := &broker.Client{}
	spaceAdvisor := space.New(space.Config{Home: appsCfg.Home, Broker: brokerClient, Run: userRun.Run, Packages: dpkgDB.Packages,
		Release: func() string {
			b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
			return strings.TrimSpace(string(b))
		}})
	spaceAdvisor.Register(reg)
	spaceAdvisor.HandleRegister(srv.API)
	pkgs.RegisterHold(reg, pkgCfg, brokerClient) // apt hold: no PackageKit API, so the broker
	docker.Register(reg, brokerClient)
	docker.RegisterLimits(reg, brokerClient)
	docker.RegisterImages(reg, brokerClient)
	docker.RegisterProjects(reg, brokerClient)
	docker.RegisterPrune(reg, brokerClient)
	docker.RegisterOrphans(reg, brokerClient)
	firewall.Register(reg, srv.API, brokerClient)
	srv.API.HandleFunc("POST /api/v1/actions/{kind}/{action}", reg.Handle)
	srv.API.HandleFunc("GET /api/v1/broker/status", brokerClient.HandleStatus)
	srv.API.HandleFunc("GET /api/v1/services/journal", services.HandleJournal(safeexec.Run, services.CanReadSystemJournal))
	srv.API.HandleFunc("GET /api/v1/journal", services.HandleJournalQuery(safeexec.Run, services.CanReadSystemJournal))
	srv.API.HandleFunc("GET /api/v1/journal/boots", services.HandleBoots(safeexec.Run))
	srv.API.HandleFunc("GET /api/v1/boot", bootinfo.Handle(safeexec.Run))

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
	srv.API.HandleFunc("GET /api/v1/containers", dockerObjs.HandleBrief)
	srv.API.HandleFunc("GET /api/v1/hardware", hardware.HandleInventory(hardware.Paths{}))
	// A USB device never seen here raises "usb.new_device" once.
	const knownUSB = "usb.known_devices"
	usbWatch := &hardware.Watcher{Log: log, Now: time.Now,
		Load: func(ctx context.Context) ([]string, bool, error) {
			raw, ok, err := db.Setting(ctx, knownUSB)
			if err != nil || !ok {
				return nil, ok, err
			}
			var list []string
			return list, true, json.Unmarshal(raw, &list)
		},
		Save: func(ctx context.Context, known []string) error {
			b, err := json.Marshal(known)
			if err != nil {
				return err
			}
			return db.SetSetting(ctx, knownUSB, b)
		},
		Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
			engine.Observe(ctx, now, hardware.Metric, v)
		}}
	dockerWG.Add(1)
	go func() { defer dockerWG.Done(); usbWatch.Run(dockerCtx) }()
	srv.API.HandleFunc("GET /api/v1/logs", logview.New(brokerClient, safeexec.Run).Handle)
	diskHealth := smart.New(smart.Config{Broker: brokerClient, Log: log,
		Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
			engine.Observe(ctx, now, smart.Metric, v)
		}})
	diskHealth.Register(reg)
	srv.API.HandleFunc("GET /api/v1/disks/health", diskHealth.Handle)
	forecasts := forecast.New(forecast.Config{DB: db, Log: log,
		Mounts: func() []forecast.Mount {
			s, ok := sampler.Ring().Last()
			if !ok {
				return nil
			}
			out := make([]forecast.Mount, 0, len(s.Disks))
			for _, d := range s.Disks {
				out = append(out, forecast.Mount{Mount: d.Mount, Total: d.Total, Avail: d.Avail})
			}
			return out
		},
		Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
			engine.Observe(ctx, now, forecast.Metric, v)
		}})
	srv.API.HandleFunc("GET /api/v1/forecasts", forecasts.Handle)
	dockerWG.Add(1)
	go func() { defer dockerWG.Done(); forecasts.Run(dockerCtx) }()
	dockerWG.Add(1)
	go func() { defer dockerWG.Done(); diskHealth.Run(dockerCtx) }()
	// Container usage for Home and the "container exited" alert, from the
	// cgroups; the broker is asked only when the running set changes.
	dockerMon := &docker.Monitor{B: brokerClient, Pub: srv.Hub, Log: log,
		Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
			engine.Observe(ctx, now, "container.exited", v)
		},
		Report: engine.Observe,
		Restarted: func(ctx context.Context, now time.Time, name string) {
			if err := db.AddChange(ctx, store.Change{Time: now, Source: changes.SourceContainers, Kind: changes.KindRestarted, Subject: name}); err != nil {
				log.Warn("record container restart", "err", err)
			}
		}}
	dockerWG.Add(1)
	go func() { defer dockerWG.Done(); dockerMon.Run(dockerCtx) }()
	srv.API.HandleFunc("GET /api/v1/docker/usage", dockerMon.HandleUsage)
	sleepAPI := power.NewSleep(power.SleepConfig{W: pw, Run: safeexec.Run, Broker: brokerClient, CanReadSystem: services.CanReadSystemJournal})
	sleepAPI.Register(reg)
	srv.API.HandleFunc("GET /api/v1/power/sleep", sleepAPI.Handle)
	srv.API.HandleFunc("GET /api/v1/crashes", crashes.New(crashes.Config{Run: safeexec.Run,
		CanReadSystem: services.CanReadSystemJournal,
		ContainerName: func(id string) string { name, _ := dockerMon.Name(id); return name }}).Handle)
	srv.API.HandleFunc("GET /api/v1/docker/containers/{id}", docker.HandleInspect(brokerClient))
	srv.API.HandleFunc("GET /api/v1/docker/volumes/sizes", docker.HandleVolumeSizes(brokerClient))
	images := &docker.Images{B: brokerClient, Registry: docker.NewRegistry(), Run: safeexec.Run, CacheDir: filepath.Join(paths.DataDir, "trivy")}
	docker.RegisterUpdate(reg, brokerClient, images.Registry, safeexec.Run)
	appUpdates := &stacks.UpdateNotes{Settings: db}
	stacks.Register(reg, brokerClient, images.Registry, appUpdates)
	srv.API.HandleFunc("GET /api/v1/app-health", stacks.HandleHealth(brokerClient, appUpdates))
	// App names at http://<app>.localhost:<port>/ (9.13): off until the
	// owner turns it on, 127.0.0.1 only, never Hearth.
	appProxy := &proxy.Proxy{Settings: db, HearthPort: srv.Port, Log: log,
		Targets: func(ctx context.Context) (map[string][]int, error) { return stacks.ProxyTargets(ctx, brokerClient) }}
	appProxy.Register(reg)
	srv.API.HandleFunc("GET /api/v1/proxy", appProxy.HandleStatus)
	appProxy.Start(ctx)
	defer appProxy.Stop()
	// Managed apps' alerts (9.12): an unhealthy app, a failed backup.
	appsWatchCtx, stopAppsWatch := context.WithCancel(ctx)
	appsWatchDone := make(chan struct{})
	go func() {
		stacks.Watch(appsWatchCtx, brokerClient, appUpdates, engine.Observe, 2*time.Minute)
		close(appsWatchDone)
	}()
	defer func() { stopAppsWatch(); <-appsWatchDone }()
	srv.API.HandleFunc("GET /api/v1/stacks", stacks.HandleList(brokerClient))
	srv.API.HandleFunc("GET /api/v1/stacks/{name}", stacks.HandleOne(brokerClient))
	srv.API.HandleFunc("GET /api/v1/app-backups/{name}", stacks.HandleBackups(brokerClient, safeexec.Run))
	stacks.RegisterSchedule(reg, brokerClient, safeexec.Run)
	srv.API.HandleFunc("GET /api/v1/app-backups/{name}/preview", stacks.HandleBackupPreview(brokerClient))
	importDeps := importer.Deps{Broker: brokerClient, Run: safeexec.Run}
	srv.API.HandleFunc("GET /api/v1/stacks/import/{project}", importer.Handle(importDeps))
	importer.RegisterAction(reg, importDeps)
	srv.API.HandleFunc("POST /api/v1/stacks/{name}/secrets", stacks.HandleSecretsSet(brokerClient, db))
	srv.API.HandleFunc("POST /api/v1/stacks/{name}/secrets/{key}/reveal", stacks.HandleReveal(brokerClient, db))
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

	// Scheduler: hearth-job-<id> timers. User jobs are files in
	// ~/.config/systemd/user plus the user manager over D-Bus.
	sessionBus := func(ctx context.Context) (scheduler.Bus, error) {
		c, err := dbusx.SessionBus(ctx)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	userUnits := filepath.Join(filepath.Dir(paths.ConfigDir), "systemd/user")
	// Root jobs: hearthd lists them from /etc/systemd/system; hearth-broker
	// writes them (SECURITY S13).
	systemBus := func(ctx context.Context) (scheduler.Bus, error) {
		c, err := dbusx.SystemBus(ctx)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	systemJobs := &scheduler.Store{Scope: scheduler.ScopeSystem, UnitDir: scheduler.SystemUnitDir, Bus: systemBus}
	systemJobs.W = &scheduler.SystemWriter{B: brokerClient, Exists: func(id string) bool {
		_, ok := systemJobs.Read(id)
		return ok
	}}
	jobs := &schedapi.API{
		Stores: map[string]*scheduler.Store{
			scheduler.ScopeUser: {Scope: scheduler.ScopeUser, UnitDir: userUnits, Bus: sessionBus,
				W: &scheduler.UserWriter{UnitDir: userUnits, JobsDir: filepath.Join(paths.DataDir, "jobs"), Bus: sessionBus}},
			scheduler.ScopeSystem: systemJobs,
		},
		Run: safeexec.Run, Audit: db.Audit, UID: os.Getuid(), Log: log,
		CanReadSystemJournal: services.CanReadSystemJournal,
	}
	jobs.Register(reg)
	jobStores.Store(&jobs.Stores)
	srv.API.HandleFunc("GET /api/v1/jobs", jobs.HandleList)
	srv.API.HandleFunc("GET /api/v1/jobs/check", jobs.HandleCheck)
	srv.API.HandleFunc("GET /api/v1/jobs/{scope}/{id}", jobs.HandleGet)
	srv.API.HandleFunc("GET /api/v1/jobs/{scope}/{id}/history", jobs.HandleHistory)
	srv.API.HandleFunc("POST /api/v1/jobs/{scope}", jobs.HandleSave)

	// Terminal: the user's shell in a transient unit of their systemd
	// manager, on a pty hearthd opens (SECURITY S14).
	term := terminal.New(terminal.Config{
		Bus: func(ctx context.Context) (terminal.Bus, error) {
			c, err := dbusx.SessionBus(ctx)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Audit: db.Audit, UID: os.Getuid(), Log: log,
	})
	srv.API.HandleFunc("GET /api/v1/terminal", term.HandleShell)

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
	srv.API.HandleFunc("GET /api/v1/services", units.HandleList)
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

	// Security posture: read-only checks run when the page asks; debsecan
	// only on the user's request (SECURITY S21c).
	withSystemBus := func(ctx context.Context, f func(dbusx.Bus)) {
		bus, err := dbusx.SystemBus(ctx)
		if err != nil {
			f(nil)
			return
		}
		defer func() { _ = bus.Close() }()
		f(bus)
	}
	srv.API.HandleFunc("GET /api/v1/hardware/firmware", hardware.HandleFirmware(withSystemBus))
	security := posture.New(posture.Config{
		UID: os.Getuid(), Path: append(filepath.SplitList(os.Getenv("PATH")), filepath.Join(appsCfg.Home, ".local/bin")),
		Sockets: func() (netinfo.Sockets, error) { return netAPI.Resolver.Read(netinfo.Dump, collect.Users()) },
		Firewalls: func(ctx context.Context) (fws []netinfo.Firewall) {
			withSystemBus(ctx, func(bus dbusx.Bus) { fws = netinfo.ReadFirewalls(ctx, bus, netinfo.FirewallConfig{}) })
			return fws
		},
		UnitActive: func(ctx context.Context, unit string) (on bool) {
			withSystemBus(ctx, func(bus dbusx.Bus) { on = netinfo.UnitActive(ctx, bus, unit) })
			return on
		},
		Updates: func(ctx context.Context) ([]string, int, error) {
			list, err := updates.List(ctx)
			var sec []string
			for _, u := range list {
				if u.Security {
					sec = append(sec, u.Name)
				}
			}
			return sec, len(list), err
		},
		Containers: func(ctx context.Context) ([]posture.Risky, error) { return docker.Risks(ctx, brokerClient) },
		Apps:       func(ctx context.Context) ([]posture.App, error) { return stacks.DangerApps(ctx, brokerClient) },
		Release: func() string {
			b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
			return strings.TrimSpace(string(b))
		},
		Needrestart: func(ctx context.Context) (posture.RestartScan, error) {
			var r broker.NeedrestartResult
			err := brokerClient.Call(ctx, "system.needrestart", nil, &r)
			return posture.RestartScan{Installed: r.Installed, Services: r.Services, Sessions: r.Sessions}, err
		},
		Run: posture.Runner(safeexec.Run), Settings: db, Log: log,
	})
	security.Register(srv.API, db)
	lynisAudits := lynis.New(lynis.Config{Broker: brokerClient, Store: db, Audit: db, Log: log,
		Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
			engine.Observe(ctx, now, lynis.Metric, v)
		}})
	lynisAudits.Register(srv.API)
	dockerWG.Add(1)
	go func() { defer dockerWG.Done(); lynisAudits.Run(dockerCtx) }()
	if home, err := os.UserHomeDir(); err != nil {
		log.Warn("integrity watch off: no home directory", "err", err)
	} else {
		watch := integrity.New(integrity.Config{Store: db, Audit: db, Home: home, Log: log,
			Observe: func(ctx context.Context, now time.Time, v map[string]float64) {
				engine.Observe(ctx, now, integrity.Metric, v)
			}})
		watch.Register(srv.API)
		dockerWG.Add(1)
		go func() { defer dockerWG.Done(); watch.Run(dockerCtx) }()
	}

	srv.API.HandleFunc("GET /api/v1/security/access", access.New(access.Config{Run: safeexec.Run,
		CanReadSystem: services.CanReadSystemJournal,
		Bus:           func(ctx context.Context) (access.Bus, error) { return dbusx.SystemBus(ctx) }}).Handle)

	// Change timeline: dpkg's log, hourly snapshots of units, containers,
	// listeners and packages (stored only when something changed), and
	// the audit log.
	tracker := changes.New(changes.Config{Store: db, Log: log, Readers: map[string]changes.Reader{
		changes.SourcePackages: func(context.Context) (map[string]string, error) { return dpkgDB.Versions() },
		changes.SourceUnits: func(ctx context.Context) (map[string]string, error) {
			out := map[string]string{}
			for scope, dial := range map[string]func(context.Context) (*dbusx.Conn, error){"system": dbusx.SystemBus, "user": dbusx.SessionBus} {
				bus, err := dial(ctx)
				if err != nil {
					return nil, err
				}
				files, err := services.UnitFiles(ctx, bus)
				_ = bus.Close()
				if err != nil {
					return nil, err
				}
				for name, state := range files {
					out[scope+"/"+name] = state
				}
			}
			return out, nil
		},
		changes.SourceContainers: func(ctx context.Context) (map[string]string, error) {
			return docker.ContainerImages(ctx, brokerClient)
		},
		changes.SourceApps: func(ctx context.Context) (map[string]string, error) {
			return stacks.Snapshot(ctx, brokerClient)
		},
		changes.SourceListeners: func(context.Context) (map[string]string, error) {
			socks, err := netAPI.Resolver.Read(netinfo.Dump, collect.Users())
			if err != nil {
				return nil, err
			}
			return netinfo.ListenerSet(socks), nil
		},
	}})
	trackerCtx, stopTracker := context.WithCancel(ctx)
	trackerDone := make(chan struct{})
	go func() { tracker.Run(trackerCtx, time.Hour); close(trackerDone) }()
	defer func() { stopTracker(); <-trackerDone }()
	tracker.Register(srv.API)

	// Modes: TOML files the user edits, untrusted, reloaded on change. An
	// invalid file is ignored and listed with its reason; a changed file
	// disarms its triggers (SECURITY S15b). Profiles are kept as modes
	// (ROADMAP 10.2): the profile engine below reads and writes mode files.
	var profileManager *profiles.Manager
	modeSet := modes.NewSet(modes.Config{Dir: filepath.Join(paths.ConfigDir, "modes"), Store: db, Log: log,
		Check: func(items []modes.Item, game bool) error {
			list := make([]profiles.Item, len(items))
			for i, it := range items {
				list[i] = profiles.Item(it)
			}
			return profileManager.ValidateItems(list, game)
		}})
	modeProfiles := &modes.ProfileStore{Set: modeSet, DB: db}

	// Profiles: sets of things to start or stop together, by hand or while
	// a game runs (only then is the running state polled).
	profileManager = profiles.New(profiles.Config{Store: modeProfiles, Actions: reg, Log: log,
		Playing: games.Playing,
		Notify: func(ctx context.Context, summary, body string) {
			if err := notifier.Notify(ctx, summary, body, "info"); err != nil {
				log.Debug("profiles: notification", "err", err)
			}
		},
		Active: func(ctx context.Context, it profiles.Item) (bool, error) {
			switch it.Kind {
			case profiles.KindContainer:
				return docker.Running(ctx, brokerClient, it.Target)
			case profiles.KindProject:
				return docker.ProjectRunning(ctx, brokerClient, it.Target)
			case profiles.KindApp:
				_, ok := apps.Running(running())[it.Target]
				return ok, nil
			}
			scope, name, err := unitctl.Target(it.Target)
			if err != nil {
				return false, err
			}
			dial := dbusx.SystemBus
			if scope == services.ScopeUser {
				dial = dbusx.SessionBus
			}
			bus, err := dial(ctx)
			if err != nil {
				return false, err
			}
			defer func() { _ = bus.Close() }()
			return netinfo.UnitActive(ctx, bus, name), nil
		},
	})
	profileManager.Register(reg)
	profileManager.HandleRegister(srv.API)
	// Profiles left in the database move to mode files once, before either
	// loop runs; one that cannot move stays and is tried at the next start.
	if n, err := modes.Migrate(ctx, db, modeSet, os.Getuid()); err != nil {
		log.Warn("modes: some profiles were not migrated", "moved", n, "err", err)
	} else if n > 0 {
		log.Info("modes: profiles migrated to mode files", "count", n)
	}
	modeSet.HandleRegister(srv.API, db)
	modesCtx, stopModes := context.WithCancel(ctx)
	modesDone := make(chan struct{})
	go func() { modeSet.Run(modesCtx); close(modesDone) }()
	defer func() { stopModes(); <-modesDone }()
	profilesCtx, stopProfiles := context.WithCancel(ctx)
	profilesDone := make(chan struct{})
	go func() { profileManager.Run(profilesCtx, 15*time.Second); close(profilesDone) }()
	defer func() { stopProfiles(); <-profilesDone }()

	// Reports: hourly usage per app, service and container from the
	// cgroups, and a weekly machine report saved when enabled.
	usage := collect.NewUsageRecorder(collect.UsageConfig{UID: os.Getuid(), Store: db, Log: log,
		Name: func(id string) (string, bool) {
			kind, rest, _ := strings.Cut(id, ":")
			switch kind {
			case collect.KindApp:
				return apps.Name(rest)
			case collect.KindContainer:
				return dockerMon.Name(rest)
			}
			return "", false
		}})
	usageCtx, stopUsage := context.WithCancel(ctx)
	usageDone := make(chan struct{})
	go func() { usage.Run(usageCtx); close(usageDone) }()
	defer func() { stopUsage(); <-usageDone }()
	// A sampler of its own: the main one's last sample may be 15 s old.
	probe := collect.New(collect.Config{Log: log, Ring: store.NewRing[collect.Sample](1)})
	insight.New(insight.Config{Store: db, Samples: sampler.Ring().Snapshot, Probe: probe.Collect, Busy: usage.Now}).Register(srv.API)
	reportBuilder := reports.New(reports.Config{Store: db, Now: func(context.Context) reports.Now {
		return reportNow(sampler, pw)
	}, Posture: security.Scan, Apps: func(ctx context.Context) ([]reports.App, error) {
		return stacks.ReportApps(ctx, brokerClient, appUpdates)
	}, Timeline: func(ctx context.Context, from, to time.Time, limit int) ([]changes.Event, error) {
		events, _, err := tracker.Timeline(ctx, from, to, nil, limit)
		return events, err
	}, Markers: func(ctx context.Context, from, to time.Time) ([]int64, error) {
		ms, err := tracker.Markers(ctx, from, to)
		if err != nil {
			return nil, err
		}
		out := make([]int64, len(ms))
		for i, m := range ms {
			out[i] = m.Time
		}
		return out, nil
	}})
	weekly := &reports.Weekly{B: reportBuilder, Settings: db, Dir: filepath.Join(paths.DataDir, "reports"), Log: log}
	weeklyCtx, stopWeekly := context.WithCancel(ctx)
	weeklyDone := make(chan struct{})
	go func() { weekly.Run(weeklyCtx, time.Hour); close(weeklyDone) }()
	defer func() { stopWeekly(); <-weeklyDone }()
	(&reports.API{B: reportBuilder, Weekly: weekly, Audit: db}).Register(srv.API)

	backups := backup.New(backup.Config{
		DB: db, Dir: filepath.Join(paths.DataDir, "backups"), ValidateProfile: profileManager.ValidateImported, Profiles: modeProfiles,
		Weekly:       reports.Toggle{W: weekly},
		RulesChanged: func(ctx context.Context) { _ = engine.Reload(ctx) },
		TilesChanged: checker.Changed, Log: log,
	})
	backups.Register(reg)
	srv.API.HandleFunc("GET /api/v1/backup", backups.HandleList)
	srv.API.HandleFunc("GET /api/v1/backup/export", backups.HandleExport)
	backupCtx, stopBackup := context.WithCancel(ctx)
	backupDone := make(chan struct{})
	go func() { backups.Run(backupCtx, time.Hour); close(backupDone) }()
	defer func() { stopBackup(); <-backupDone }()

	if serverHook != nil {
		serverHook(srv) // every route is registered by now
	}
	if registryHook != nil {
		registryHook(reg)
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

// startWait bounds how long `hearthd open` waits for a started daemon.
const startWait = 5 * time.Second

// openHold keeps `hearthd open` alive after the link is handed over, so
// gnome-shell can still place it in its app scope.
const openHold = time.Second

// openUI is `hearthd open`: make sure the daemon runs, then open a fresh
// login link. It never serves anything itself: a daemon that is down is
// started by the user manager, so there is never a second one. Any failure
// reaches the user as a notification (the launcher has no terminal).
func openUI(ctx context.Context, paths Paths) error {
	cfg := launcherConfig()
	_, statErr := os.Stat(paths.ConfigFile())
	port, err := ensureDaemon(ctx, paths)
	if err != nil {
		launcher.Notify(ctx, cfg, "Hearth could not start", err.Error()+"\nTry: systemctl --user start hearthd")
		return err
	}
	if statErr != nil {
		return nil // a first run: the daemon opens the browser itself
	}
	u, err := launchURL(paths, port)
	if err != nil {
		launcher.Notify(ctx, cfg, "Hearth could not start", err.Error())
		return err
	}
	if err := launcher.Open(ctx, cfg, u); err != nil {
		return err
	}
	select {
	case <-time.After(openHold):
	case <-ctx.Done():
	}
	return nil
}

// ensureDaemon returns the running daemon's port, starting hearthd.service
// if needed. A daemon writes a fresh token before it listens, so once it
// answers, the token file is its own.
func ensureDaemon(ctx context.Context, paths Paths) (int, error) {
	up := func() (int, bool) {
		b, err := os.ReadFile(paths.ConfigFile())
		if err != nil {
			return 0, false
		}
		cfg, err := parseConfig(b)
		if err != nil || checkHealth(ctx, cfg.Port) != nil {
			return 0, false
		}
		return cfg.Port, true
	}
	if port, ok := up(); ok {
		return port, nil
	}
	conn, err := dbusx.SessionBus(ctx)
	if err != nil {
		return 0, fmt.Errorf("cannot reach your session: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var job dbus.ObjectPath
	if err := conn.Call(ctx, "org.freedesktop.systemd1", "/org/freedesktop/systemd1",
		"org.freedesktop.systemd1.Manager.StartUnit", []any{&job}, "hearthd.service", "replace"); err != nil {
		return 0, fmt.Errorf("cannot start hearthd: %w", err)
	}
	for deadline := time.Now().Add(startWait); time.Now().Before(deadline); {
		if port, ok := up(); ok {
			return port, nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return 0, errors.New("hearthd did not start in time")
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

// launcherConfig reaches the user's desktop. Inside hearthd.service
// (INVOCATION_ID is set) only the portal can start a browser outside the
// sandbox.
func launcherConfig() launcher.Config {
	return launcher.Config{
		Session: func(ctx context.Context) (launcher.Bus, error) {
			c, err := dbusx.SessionBus(ctx)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Start:      func(name string, args ...string) error { return safeexec.Start(name, openHold, args...) },
		PortalOnly: os.Getenv("INVOCATION_ID") != "",
	}
}

// openBrowser opens link for the user.
func openBrowser(ctx context.Context, link string) error {
	return launcher.Open(ctx, launcherConfig(), link)
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

// buildVersion is set by make (scripts/version.sh); a plain go build has none.
var buildVersion string

func version() string {
	if buildVersion != "" {
		return buildVersion
	}
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

// reportNow is the machine's current state for reports: disks from the
// latest sample, batteries from UPower.
func reportNow(sampler *collect.Sampler, pw *power.Watcher) reports.Now {
	now := reports.Now{Batteries: pw.Snapshot().Batteries}
	now.Host, _ = os.Hostname()
	if s, ok := sampler.Ring().Last(); ok {
		for _, d := range s.Disks {
			now.Disks = append(now.Disks, reports.Disk{Mount: d.Mount, Total: d.Total, Used: d.Used, Pct: d.Pct})
		}
	}
	return now
}

// splitList splits a colon-separated list, dropping empty entries.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ":") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
