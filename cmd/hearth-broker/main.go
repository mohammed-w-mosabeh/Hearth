// Command hearth-broker is Hearth's privileged helper (SECURITY S11). It
// runs as root, only when hearth-broker.socket starts it, serves the
// static method allowlist of internal/broker to members of the hearth
// group, and exits after 60 s without connections.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"syscall"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/dropin"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/scheduler"
)

// stateDir is the unit's StateDirectory= (created by systemd, mode 0700).
const stateDir = "/var/lib/hearth"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	if os.Geteuid() != 0 {
		return errors.New("hearth-broker runs as root, started by hearth-broker.socket")
	}
	ln, err := systemdListener()
	if err != nil {
		return err
	}
	var srv *broker.Server
	methods := broker.BaseMethods(version(), func() []string { return srv.Names() })
	methods = append(methods, broker.DockerMethods(broker.NewDocker(broker.DockerSocket))...)
	methods = append(methods, broker.PackageMethods(safeexec.Run)...)
	methods = append(methods, broker.SpaceMethods(safeexec.Run, broker.AptArchives)...)
	methods = append(methods, broker.SmartMethods(safeexec.Run, broker.SysRoot)...)
	methods = append(methods, broker.NeedrestartMethods(broker.NeedrestartConfig{Bus: func(ctx context.Context) (broker.SystemBus, error) {
		c, err := dbusx.SystemBus(ctx)
		if err != nil {
			return nil, err
		}
		return c, nil
	}})...)
	methods = append(methods, scheduler.BrokerMethods(scheduler.BrokerConfig{
		UnitDir: scheduler.SystemUnitDir, JobsDir: scheduler.SystemJobsDir, Run: safeexec.Run,
		Bus: func(ctx context.Context) (scheduler.Bus, error) {
			c, err := dbusx.SystemBus(ctx)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
	})...)
	methods = append(methods, dropin.BrokerMethods(dropin.SystemDir, func(ctx context.Context) (dropin.Reloader, error) {
		c, err := dbusx.SystemBus(ctx)
		if err != nil {
			return nil, err
		}
		return c, nil
	})...)
	srv, err = broker.NewServer(broker.Config{
		Methods:   methods,
		InGroup:   broker.GroupMember(broker.Group),
		Authority: broker.Polkit{Dial: dbusx.SystemBus},
		Audit:     &broker.FileAudit{Path: filepath.Join(stateDir, "audit.jsonl"), MaxBytes: 8 << 20},
		Log:       log,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("hearth-broker started", "version", version(), "methods", srv.Names())
	return srv.Serve(ctx, ln)
}

// systemdListener takes the listening socket systemd passes (LISTEN_FDS,
// fd 3). The broker never opens a socket of its own: without its socket
// unit it does not run.
func systemdListener() (net.Listener, error) {
	pid, err := strconv.Atoi(os.Getenv("LISTEN_PID"))
	if err != nil || pid != os.Getpid() || os.Getenv("LISTEN_FDS") != "1" {
		return nil, errors.New("no socket from systemd: start hearth-broker.socket, not the binary")
	}
	_ = os.Unsetenv("LISTEN_PID")
	_ = os.Unsetenv("LISTEN_FDS")
	_ = os.Unsetenv("LISTEN_FDNAMES")
	f := os.NewFile(3, "hearth-broker.socket")
	ln, err := net.FileListener(f)
	_ = f.Close() // FileListener dups it
	if err != nil {
		return nil, fmt.Errorf("socket from systemd: %w", err)
	}
	if _, ok := ln.(*net.UnixListener); !ok {
		_ = ln.Close()
		return nil, errors.New("socket from systemd is not a Unix socket")
	}
	return ln, nil
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
