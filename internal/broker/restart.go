package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const (
	// NeedrestartUnit lists what runs old libraries. It is a unit of its
	// own because that takes CAP_SYS_PTRACE, which the broker does not
	// hold: run from here, needrestart would see nothing and say all is
	// well (SECURITY S16g).
	NeedrestartUnit = "hearth-needrestart.service"
	// NeedrestartOutput is where the unit writes.
	NeedrestartOutput = "/var/lib/hearth/needrestart.out"
	// NeedrestartBinary tells whether the optional tool is there.
	NeedrestartBinary = "/usr/sbin/needrestart"

	needrestartFresh = 5 * time.Minute // a member cannot make it run more often
	maxRestartItems  = 200
	maxRestartOutput = 1 << 20
	sdDest           = "org.freedesktop.systemd1"
	sdPath           = dbus.ObjectPath("/org/freedesktop/systemd1")
	sdManager        = "org.freedesktop.systemd1.Manager"
	sdUnit           = "org.freedesktop.systemd1.Unit"
)

// NeedrestartResult is system.needrestart's result.
type NeedrestartResult struct {
	Installed bool     `json:"installed"`
	Services  []string `json:"services"` // units running old libraries
	Sessions  []string `json:"sessions"` // user sessions doing so
}

// SystemBus is what the method needs from the system bus.
type SystemBus interface {
	Call(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	GetAll(ctx context.Context, dest string, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error)
	Close() error
}

// NeedrestartConfig configures NeedrestartMethods.
type NeedrestartConfig struct {
	Bus    func(ctx context.Context) (SystemBus, error)
	Output string // default NeedrestartOutput
	Binary string // default NeedrestartBinary
	Poll   time.Duration
}

// parseNeedrestart reads `needrestart -b`. The version line is always
// printed, so output without it is a failed run, not a clean machine.
func parseNeedrestart(r io.Reader) (NeedrestartResult, error) {
	out := NeedrestartResult{Installed: true, Services: []string{}, Sessions: []string{}}
	seen := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ": ")
		if !ok {
			continue
		}
		switch key {
		case "NEEDRESTART-VER":
			seen = true
		case "NEEDRESTART-SVC":
			if safeexec.UnitName(val) == nil && len(out.Services) < maxRestartItems {
				out.Services = append(out.Services, val)
			}
		case "NEEDRESTART-SESS":
			if s := cleanText(val, 120); s != "" && len(out.Sessions) < maxRestartItems {
				out.Sessions = append(out.Sessions, s)
			}
		}
	}
	if err := sc.Err(); err != nil || !seen {
		return out, Errorf(CodeFailed, "needrestart: no usable output")
	}
	return out, nil
}

func (c NeedrestartConfig) read() (NeedrestartResult, error) {
	f, err := os.Open(c.Output) // #nosec G304 -- a fixed path in the 0700 state directory
	if err != nil {
		return NeedrestartResult{}, Errorf(CodeFailed, "needrestart: no output")
	}
	defer func() { _ = f.Close() }()
	return parseNeedrestart(io.LimitReader(f, maxRestartOutput))
}

// runUnit starts the unit and waits until it has ended after started. The
// state alone cannot tell: before the job begins it still reads inactive.
func (c NeedrestartConfig) runUnit(ctx context.Context) error {
	bus, err := c.Bus(ctx)
	if err != nil {
		return Errorf(CodeFailed, "the system bus is not available")
	}
	defer func() { _ = bus.Close() }()
	started := uint64(time.Now().UnixMicro()) // #nosec G115 -- after 1970
	var job, unit dbus.ObjectPath
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".LoadUnit", []any{&unit}, NeedrestartUnit); err != nil {
		return Errorf(CodeFailed, "needrestart: the unit is not installed")
	}
	// systemd unloads an inactive unit, and then its state cannot be read:
	// the reference holds it for as long as this bus connection lives.
	if err := bus.Call(ctx, sdDest, unit, sdUnit+".Ref", nil); err != nil {
		return Errorf(CodeFailed, "needrestart: the unit could not be held")
	}
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".StartUnit", []any{&job}, NeedrestartUnit, "replace"); err != nil {
		return Errorf(CodeFailed, "needrestart: the unit did not start")
	}
	poll := c.Poll
	if poll == 0 {
		poll = 500 * time.Millisecond
	}
	for {
		select {
		case <-ctx.Done():
			return Errorf(CodeFailed, "needrestart: took too long")
		case <-time.After(poll):
		}
		props, err := bus.GetAll(ctx, sdDest, unit, sdUnit)
		if err != nil {
			return Errorf(CodeFailed, "needrestart: its state could not be read")
		}
		state, _ := props["ActiveState"].Value().(string)
		changed, _ := props["StateChangeTimestamp"].Value().(uint64)
		if changed < started || (state != "inactive" && state != "failed") {
			continue
		}
		if state == "failed" {
			return Errorf(CodeFailed, "needrestart: it failed; see the journal of "+NeedrestartUnit)
		}
		return nil
	}
}

// NeedrestartMethods is system.needrestart (SECURITY S16g). It needs no
// polkit: it changes nothing, and it runs a fixed unit at most every five
// minutes.
func NeedrestartMethods(c NeedrestartConfig) []Method {
	if c.Output == "" {
		c.Output = NeedrestartOutput
	}
	if c.Binary == "" {
		c.Binary = NeedrestartBinary
	}
	return []Method{{
		Name: "system.needrestart", Timeout: 150 * time.Second,
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			if err := noParams(p); err != nil {
				return nil, err
			}
			if _, err := os.Stat(c.Binary); errors.Is(err, fs.ErrNotExist) {
				return NeedrestartResult{Services: []string{}, Sessions: []string{}}, nil
			}
			if fi, err := os.Stat(c.Output); err == nil && fi.Size() > 0 && time.Since(fi.ModTime()) < needrestartFresh {
				return c.read()
			}
			if err := c.runUnit(ctx); err != nil {
				return nil, err
			}
			return c.read()
		},
	}}
}
