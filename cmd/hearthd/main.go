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

	"github.com/mohammed-w-mosabeh/hearth/internal/api"
	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
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

	sampler := collect.New(collect.Config{Pub: srv.Hub, Log: log})
	samplerCtx, stopSampler := context.WithCancel(ctx)
	samplerDone := make(chan struct{})
	go func() { sampler.Run(samplerCtx); close(samplerDone) }()
	defer func() { stopSampler(); <-samplerDone }()

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
