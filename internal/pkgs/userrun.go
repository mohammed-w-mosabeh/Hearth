package pkgs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// UserRunner runs one fixed command as a transient unit of the user's
// systemd manager, and waits for it to end. User-level package managers
// (flatpak --user, npm in a user prefix, cargo, gio trash) write to the
// home directory, which is read-only in hearthd's sandbox; the user
// manager runs them as the user, outside it, with no shell.
type UserRunner struct {
	Bus    func(ctx context.Context) (Bus, error) // the session bus
	LogDir string                                 // writable by hearthd; the command's output goes here
	Wait   time.Duration                          // default 15 min (a polkit dialog included)
}

const (
	systemd   = "org.freedesktop.systemd1"
	sdPath    = dbus.ObjectPath("/org/freedesktop/systemd1")
	sdManager = "org.freedesktop.systemd1.Manager"
	maxRunLog = 16 << 10
)

// D-Bus shapes of StartTransientUnit (ssa(sv)a(sa(sv))).
type (
	unitProp struct {
		Name  string
		Value dbus.Variant
	}
	auxUnit struct {
		Name  string
		Props []unitProp
	}
	execEx struct { // ExecStartEx a(sasas)
		Path  string
		Argv  []string
		Flags []string
	}
)

// RunError is a command that ended badly; Output is the end of what it
// printed.
type RunError struct {
	Result string // systemd's job result, e.g. "failed"
	Output string
}

func (e *RunError) Error() string {
	if e.Output == "" {
		return "the command " + e.Result
	}
	return e.Output
}

// Run runs path with argv (argv[0] is the program name) and env, and
// returns its output.
func (u *UserRunner) Run(ctx context.Context, desc, path string, argv, env []string) (string, error) {
	if !filepath.IsAbs(path) || len(argv) == 0 {
		return "", errors.New("pkgs: bad command")
	}
	wait := u.Wait
	if wait == 0 {
		wait = 15 * time.Minute
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	unit := "hearth-pkg-" + hex.EncodeToString(rnd[:]) + ".service"
	if err := os.MkdirAll(u.LogDir, 0o700); err != nil {
		return "", err
	}
	logFile := filepath.Join(u.LogDir, strings.TrimSuffix(unit, ".service")+".log")
	defer func() { _ = os.Remove(logFile) }()

	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	bus, err := u.Bus(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot reach your systemd user manager: %w", err)
	}
	defer func() { _ = bus.Close() }()
	// Subscribe before starting, so the job's end cannot be missed.
	sigs, err := bus.Watch(ctx, dbus.WithMatchObjectPath(sdPath), dbus.WithMatchInterface(sdManager), dbus.WithMatchMember("JobRemoved"))
	if err != nil {
		return "", err
	}
	if err := bus.Call(ctx, systemd, sdPath, sdManager+".Subscribe", nil); err != nil {
		return "", err
	}
	props := []unitProp{
		{"Description", dbus.MakeVariant(desc + " (started by Hearth)")},
		// oneshot: the job ends when the command does, with its result.
		{"Type", dbus.MakeVariant("oneshot")},
		{"CollectMode", dbus.MakeVariant("inactive-or-failed")},
		{"ExecStartEx", dbus.MakeVariant([]execEx{{Path: path, Argv: argv, Flags: []string{"no-env-expand"}}})},
		{"Environment", dbus.MakeVariant(env)},
		{"StandardInput", dbus.MakeVariant("null")},
		// stdout to the file; stderr follows stdout.
		{"StandardOutputFileToTruncate", dbus.MakeVariant(logFile)},
	}
	var job dbus.ObjectPath
	if err := bus.Call(ctx, systemd, sdPath, sdManager+".StartTransientUnit", []any{&job}, unit, "fail", props, []auxUnit{}); err != nil {
		return "", fmt.Errorf("the command could not be started: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("still running after %s; see journalctl --user -u %s", wait, unit)
		case s, ok := <-sigs:
			if !ok {
				return "", errors.New("lost the connection to systemd")
			}
			if len(s.Body) != 4 {
				continue
			}
			if p, _ := s.Body[1].(dbus.ObjectPath); p != job {
				continue
			}
			out := tail(logFile)
			if result, _ := s.Body[3].(string); result != "done" {
				return out, &RunError{Result: result, Output: out}
			}
			return out, nil
		}
	}
}

// tail is the last maxRunLog bytes of the command's output.
func tail(path string) string {
	f, err := os.Open(path) // #nosec G304 -- our own log file
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxRunLog {
		_, _ = f.Seek(-maxRunLog, io.SeekEnd)
	}
	b, _ := io.ReadAll(io.LimitReader(f, maxRunLog))
	return strings.TrimSpace(strings.ToValidUTF8(string(b), ""))
}
