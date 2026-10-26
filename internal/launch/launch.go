// Package launch starts applications for the user (SPEC 2, ROADMAP 1.11).
//
// Each launch is a transient service of the systemd user manager,
// app-hearth-<id>@<random>.service in app.slice. The name follows
// systemd's desktop naming (systemd.special(7)), so Hearth's process view
// and the desktop both see it as the app, and its usage is measurable and
// stoppable as a group. A transient service rather than a scope: a scope
// adopts a process hearthd started itself, which would inherit the
// hearthd.service sandbox (read-only home, private /tmp); the user
// manager starts a service fresh. Nothing goes through a shell or os/exec
// (SECURITY S7): the argv parsed from Exec= is handed to systemd over
// D-Bus, with environment expansion off.
package launch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const (
	systemd = "org.freedesktop.systemd1"
	manager = "org.freedesktop.systemd1.Manager"
	objPath = dbus.ObjectPath("/org/freedesktop/systemd1")
)

// Bus is a closable D-Bus connection (dbusx.Conn in production).
type Bus interface {
	dbusx.Bus
	Close() error
}

// Apps finds an app by ID (discovery.Index in production).
type Apps interface {
	App(id string) (discovery.App, bool)
}

// Config configures the launcher.
type Config struct {
	Apps Apps
	// Bus connects to the user's session bus; one connection per launch.
	Bus  func(ctx context.Context) (Bus, error)
	Home string
	// Path is searched for programs named without a directory; Home's
	// ~/.local/bin is searched after it.
	Path []string
	// Terminal runs Terminal=true apps as Terminal -e <argv>; default
	// Debian's x-terminal-emulator alternative.
	Terminal string
	// Wait bounds how long a start may take to be confirmed; default 10 s.
	Wait time.Duration
}

// shells are refused as the program of an entry: Hearth never runs shell
// command lines (SECURITY S7), even ones a .desktop file asks for.
var shells = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "mksh": true, "fish": true, "csh": true, "tcsh": true}

type launcher struct{ cfg Config }

// command is what a launch will run, from the plan to Run.
type command struct {
	prog string   // absolute path
	argv []string // argv[0] as the entry wrote it
	dir  string
	name string
}

// Register adds app/launch to reg.
func Register(reg *actions.Registry, cfg Config) {
	if cfg.Terminal == "" {
		cfg.Terminal = "/usr/bin/x-terminal-emulator"
	}
	if cfg.Wait == 0 {
		cfg.Wait = 10 * time.Second
	}
	l := &launcher{cfg: cfg}
	reg.Register(actions.Action{Kind: "app", Name: "launch", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if !discovery.ValidID(r.Target) || len(r.Params) != 0 {
				return errors.New(`target must be an application ID and params empty`)
			}
			return nil
		},
		Plan: l.plan, Run: l.run})
}

func (l *launcher) plan(_ context.Context, r actions.Request) (actions.Plan, error) {
	a, ok := l.cfg.Apps.App(r.Target)
	if !ok {
		return actions.Plan{}, actions.Errorf(http.StatusNotFound, "unknown application")
	}
	cmd, err := l.command(a)
	if err != nil {
		return actions.Plan{}, actions.Errorf(http.StatusUnprocessableEntity, "%s cannot be started: %v", a.Name, err)
	}
	return actions.Plan{
		Summary: "launch " + a.Name,
		Items:   []actions.Item{{ID: a.ID, Name: a.Name, Detail: strings.Join(cmd.argv, " ")}},
		Blocked: []actions.Blocked{},
		State:   cmd,
	}, nil
}

// command resolves what launching a runs.
func (l *launcher) command(a discovery.App) (command, error) {
	var argv []string
	if a.Source == discovery.SourceAppImage {
		argv = []string{a.Path} // the file itself; its name is not an Exec line
	} else {
		var err error
		if argv, err = parseExec(a.Exec, fields{Name: a.Name, Icon: a.IconName, Path: a.Path}); err != nil {
			return command{}, err
		}
	}
	for _, s := range argv {
		if strings.ContainsFunc(s, unicode.IsControl) {
			return command{}, errors.New("the Exec line contains control characters")
		}
	}
	prog, err := l.resolve(argv[0])
	if err != nil {
		return command{}, err
	}
	if runsShell(prog, argv) {
		return command{}, errors.New("it runs a shell command line, which Hearth does not start")
	}
	if a.Terminal {
		term, err := l.resolve(l.cfg.Terminal)
		if err != nil {
			return command{}, errors.New("it needs a terminal and no terminal emulator is installed")
		}
		argv = append([]string{l.cfg.Terminal, "-e"}, argv...)
		prog = term
	}
	dir := l.cfg.Home
	if a.WorkDir != "" && filepath.IsAbs(a.WorkDir) {
		if fi, err := os.Stat(a.WorkDir); err == nil && fi.IsDir() {
			dir = filepath.Clean(a.WorkDir)
		}
	}
	return command{prog: prog, argv: argv, dir: dir, name: a.Name}, nil
}

// runsShell reports whether the command is a shell, directly or through env.
func runsShell(prog string, argv []string) bool {
	if shells[filepath.Base(prog)] {
		return true
	}
	if filepath.Base(prog) != "env" {
		return false
	}
	for _, s := range argv[1:] {
		if strings.HasPrefix(s, "-") || strings.Contains(s, "=") {
			continue
		}
		return shells[filepath.Base(s)]
	}
	return false
}

// resolve finds the program an Exec line names, as the spec asks: an
// absolute path, or a name looked up in $PATH.
func (l *launcher) resolve(name string) (string, error) {
	isExec := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
	}
	if filepath.IsAbs(name) {
		p := filepath.Clean(name)
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s does not exist", p)
		}
		if !isExec(p) {
			return "", fmt.Errorf("%s is not executable (mark it executable first)", p)
		}
		return p, nil
	}
	if strings.Contains(name, "/") {
		return "", fmt.Errorf("the program %q is a relative path", name)
	}
	dirs := append(append([]string{}, l.cfg.Path...), filepath.Join(l.cfg.Home, ".local/bin"))
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue // a relative $PATH entry would depend on hearthd's directory
		}
		if p := filepath.Join(d, name); isExec(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("the program %q is not installed", name)
}

// unitName is app-hearth-<escaped id>@<random>.service.
func unitName(id string) (string, error) {
	var r [4]byte
	if _, err := rand.Read(r[:]); err != nil {
		return "", err
	}
	u := "app-hearth-" + escapeUnit(id) + "@" + hex.EncodeToString(r[:]) + ".service"
	if err := safeexec.UnitName(u); err != nil {
		return "", fmt.Errorf("the application ID does not fit a unit name: %w", err)
	}
	return u, nil
}

// escapeUnit escapes s for a unit name as systemd-escape does: "-" and
// anything outside [A-Za-z0-9:_.] (and a leading ".") become \xNN.
func escapeUnit(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String()
}

// D-Bus argument shapes of StartTransientUnit (ssa(sv)a(sa(sv))).
type (
	property struct {
		Name  string
		Value dbus.Variant
	}
	auxUnit struct {
		Name  string
		Props []property
	}
	execEx struct { // ExecStartEx a(sasas)
		Path  string
		Argv  []string
		Flags []string
	}
)

func (l *launcher) run(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
	cmd, ok := p.State.(command)
	if !ok {
		return actions.Result{}, errors.New("launch: missing plan")
	}
	unit, err := unitName(r.Target)
	if err != nil {
		return actions.Result{}, actions.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	bus, err := l.cfg.Bus(ctx)
	if err != nil {
		return actions.Result{}, actions.Errorf(http.StatusServiceUnavailable, "cannot reach your systemd user manager: %v", err)
	}
	defer func() { _ = bus.Close() }()

	wctx, cancel := context.WithTimeout(ctx, l.cfg.Wait)
	defer cancel()
	// Subscribe before starting, so the job's end cannot be missed.
	sigs, err := bus.Watch(wctx, dbus.WithMatchObjectPath(objPath), dbus.WithMatchInterface(manager), dbus.WithMatchMember("JobRemoved"))
	if err != nil {
		return actions.Result{}, err
	}
	if err := bus.Call(wctx, systemd, objPath, manager+".Subscribe", nil); err != nil {
		return actions.Result{}, err
	}
	props := []property{
		{"Description", dbus.MakeVariant(cmd.name + " (started by Hearth)")},
		// exec: the job ends once the program is running (or failed to).
		{"Type", dbus.MakeVariant("exec")},
		// The app lives as long as any of its processes, even if the
		// first one exits after spawning the real window.
		{"ExitType", dbus.MakeVariant("cgroup")},
		{"CollectMode", dbus.MakeVariant("inactive-or-failed")},
		{"Slice", dbus.MakeVariant("app.slice")},
		{"WorkingDirectory", dbus.MakeVariant(cmd.dir)},
		{"ExecStartEx", dbus.MakeVariant([]execEx{{Path: cmd.prog, Argv: cmd.argv, Flags: []string{"no-env-expand"}}})},
	}
	var job dbus.ObjectPath
	if err := bus.Call(wctx, systemd, objPath, manager+".StartTransientUnit", []any{&job}, unit, "fail", props, []auxUnit{}); err != nil {
		return actions.Result{}, actions.Errorf(http.StatusBadGateway, "%s could not be started: %v", cmd.name, err)
	}
	for {
		select {
		case <-wctx.Done():
			return actions.Result{}, actions.Errorf(http.StatusGatewayTimeout, "%s did not start within %s", cmd.name, l.cfg.Wait)
		case s, ok := <-sigs:
			if !ok {
				return actions.Result{}, actions.Errorf(http.StatusBadGateway, "lost the connection to systemd while starting %s", cmd.name)
			}
			if len(s.Body) != 4 {
				continue
			}
			if path, _ := s.Body[1].(dbus.ObjectPath); path != job {
				continue
			}
			if result, _ := s.Body[3].(string); result != "done" {
				return actions.Result{}, actions.Errorf(http.StatusBadGateway,
					"%s could not be started (%s); see journalctl --user -u '%s'", cmd.name, result, unit)
			}
			return actions.Result{Done: 1, Failed: []actions.Blocked{}}, nil
		}
	}
}
