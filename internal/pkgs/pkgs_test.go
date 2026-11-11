package pkgs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
)

// sig is a scripted transaction signal.
type sig struct {
	member string
	body   []any
}

func pkg(info uint32, id string) sig { return sig{"Package", []any{info, id, "summary of " + id}} }

var finished = sig{"Finished", []any{uint32(1), uint32(10)}}

// fakePK plays PackageKit: each transaction method answers with the
// signals script returns for it.
type fakePK struct {
	mu     sync.Mutex
	calls  []string
	script func(method string, args []any) []sig
	n      int
	ch     chan *dbus.Signal
	hints  [][]string
}

func (f *fakePK) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	return nil, nil
}
func (f *fakePK) Set(context.Context, string, dbus.ObjectPath, string, string, any) error { return nil }
func (f *fakePK) Close() error                                                            { return nil }

func (f *fakePK) Watch(context.Context, ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	return f.ch, nil
}

func (f *fakePK) Call(_ context.Context, _ string, path dbus.ObjectPath, method string, out []any, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case method == "org.freedesktop.PackageKit.CreateTransaction":
		f.n++
		*(out[0].(*dbus.ObjectPath)) = dbus.ObjectPath(fmt.Sprintf("/tx/%d", f.n))
		f.ch = make(chan *dbus.Signal, 64)
		return nil
	case strings.HasSuffix(method, ".SetHints"):
		f.hints = append(f.hints, args[0].([]string))
		return nil
	}
	short := strings.TrimPrefix(method, pkTx+".")
	f.calls = append(f.calls, fmt.Sprint(short, " ", args))
	for _, s := range append(f.script(short, args), finished) {
		f.ch <- &dbus.Signal{Path: path, Name: pkTx + "." + s.member, Body: s.body}
	}
	return nil
}

func (f *fakePK) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type fakeDpkg map[string]discovery.Package

func (d fakeDpkg) ByName(name string) ([]discovery.Package, error) {
	if p, ok := d[name]; ok {
		return []discovery.Package{p}, nil
	}
	return nil, nil
}

var dpkgDB = fakeDpkg{
	"bash":    {Name: "bash", Arch: "amd64", Essential: true, Priority: "required"},
	"libc6":   {Name: "libc6", Arch: "amd64", Priority: "optional", Protected: true},
	"cowsay":  {Name: "cowsay", Arch: "all", Priority: "optional"},
	"fortune": {Name: "fortune", Arch: "amd64", Priority: "optional"},
	"vim":     {Name: "vim", Arch: "amd64", Priority: "optional"},
	"hearth":  {Name: "hearth", Arch: "amd64", Priority: "optional"},
}

const (
	cowsay  = "cowsay;3.03;all;manual:debian"
	fortune = "fortune;1.99;amd64;auto:debian"
	vim     = "vim;9.1;amd64;manual:debian"
	libc    = "libc6;2.41;amd64;auto:debian"
)

func setup(script func(string, []any) []sig) (Config, *fakePK) {
	f := &fakePK{script: script}
	return Config{PK: &Client{Dial: func(context.Context) (Bus, error) { return f, nil }}, Dpkg: dpkgDB}, f
}

func status(err error) int {
	var e *actions.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestTargets(t *testing.T) {
	for _, ok := range []string{"bash", "libc6:amd64", "g++", "libstdc++6", "python3.13"} {
		if _, _, err := Target(ok); err != nil {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "a", "Bash", "-x", "a b", "a;b;c;d", "../x", "x:AMD64", "x:", "x\n"} {
		if _, _, err := Target(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTransactionSignals(t *testing.T) {
	cfg, _ := setup(func(m string, _ []any) []sig {
		switch m {
		case "Resolve":
			return []sig{pkg(1, cowsay), pkg(1, "garbage"), pkg(1, cowsay)}
		case "GetUpdates":
			return []sig{{"ErrorCode", []any{uint32(48), "Not authorized"}}}
		}
		return nil
	})
	got, err := cfg.PK.Resolve(context.Background(), true, []string{"cowsay"})
	if err != nil || len(got) != 2 || got[0].Name != "cowsay" || got[0].Info != "installed" || got[0].Arch != "all" {
		t.Fatalf("%v %+v", err, got)
	}
	var te *TxError
	if _, err := cfg.PK.Updates(context.Background()); !errors.As(err, &te) || te.Code != "not-authorized" {
		t.Fatalf("%v", err)
	}
}

func TestRemoveProtectedTargetNeverAsksPackageKit(t *testing.T) {
	for target, reason := range map[string]string{"bash": ReasonEssential, "libc6": ReasonProtected, "hearth": ReasonSelf} {
		cfg, f := setup(func(string, []any) []sig { return nil })
		p, err := plan(context.Background(), cfg, OpRemove, target)
		if err != nil || len(p.Items) != 0 || len(p.Blocked) != 1 || p.Blocked[0].Reason != reason || len(f.called()) != 0 {
			t.Errorf("%s: %v %+v %v", target, err, p, f.called())
		}
	}
}

func TestRemoveThatTakesAProtectedPackageIsBlocked(t *testing.T) {
	cfg, _ := setup(func(m string, _ []any) []sig {
		if m == "Resolve" {
			return []sig{pkg(1, vim)}
		}
		return []sig{pkg(13, vim), pkg(13, libc)}
	})
	p, err := plan(context.Background(), cfg, OpRemove, "vim")
	if err != nil || len(p.Items) != 0 || len(p.Blocked) != 1 || p.Blocked[0].Name != "libc6" || p.Blocked[0].Reason != ReasonProtected {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestRemovePlans(t *testing.T) {
	cfg, f := setup(func(m string, args []any) []sig {
		if m == "Resolve" {
			return []sig{pkg(1, cowsay)}
		}
		return []sig{pkg(13, cowsay), pkg(13, fortune)}
	})
	p, err := plan(context.Background(), cfg, OpRemove, "cowsay")
	if err != nil || len(p.Items) != 2 || p.Confirm != "cowsay" || len(p.Blocked) != 0 {
		t.Fatalf("%v %+v", err, p)
	}
	if c := f.called(); len(c) != 2 || !strings.HasPrefix(c[1], "RemovePackages [4 ["+cowsay+"] true false]") {
		t.Fatalf("calls %v", c) // simulate flag, allow deps, no autoremove
	}
}

func TestInstallThatRemovesNeedsTypedName(t *testing.T) {
	cfg, _ := setup(func(m string, _ []any) []sig {
		if m == "Resolve" {
			return []sig{pkg(2, "nano;8.4;amd64;debian")}
		}
		return []sig{pkg(12, "nano;8.4;amd64;debian"), pkg(13, vim)}
	})
	p, err := plan(context.Background(), cfg, OpInstall, "nano")
	if err != nil || len(p.Items) != 2 || p.Confirm != "nano" {
		t.Fatalf("%v %+v", err, p)
	}
	cfg, _ = setup(func(m string, _ []any) []sig {
		if m == "Resolve" {
			return []sig{pkg(2, "nano;8.4;amd64;debian")}
		}
		return []sig{pkg(12, "nano;8.4;amd64;debian")}
	})
	if p, err := plan(context.Background(), cfg, OpInstall, "nano"); err != nil || len(p.Items) != 1 || p.Confirm != "" {
		t.Fatalf("plain install: %v %+v", err, p)
	}
}

func TestNotFoundAndSimulationErrors(t *testing.T) {
	cfg, _ := setup(func(string, []any) []sig { return nil })
	if _, err := plan(context.Background(), cfg, OpUpdate, "cowsay"); status(err) != http.StatusNotFound {
		t.Fatalf("no update: %v", err)
	}
	cfg, _ = setup(func(m string, _ []any) []sig {
		if m == "Resolve" {
			return []sig{pkg(1, vim)}
		}
		return []sig{{"ErrorCode", []any{uint32(13), "unmet dependencies"}}}
	})
	if _, err := plan(context.Background(), cfg, OpRemove, "vim"); status(err) != http.StatusConflict || !strings.Contains(err.Error(), "unmet dependencies") {
		t.Fatalf("dep failure: %v", err)
	}
}

func TestRunChecksThePreviewAgain(t *testing.T) {
	extra := false
	authorized := true
	cfg, f := setup(func(m string, args []any) []sig {
		switch {
		case m == "Resolve":
			return []sig{pkg(1, cowsay)}
		case args[0].(uint64)&flagSimulate != 0:
			if extra {
				return []sig{pkg(13, cowsay), pkg(13, fortune)}
			}
			return []sig{pkg(13, cowsay)}
		case !authorized:
			return []sig{{"ErrorCode", []any{uint32(48), "Not authorized"}}}
		}
		return []sig{pkg(13, cowsay)}
	})
	ctx := context.Background()
	p, err := plan(ctx, cfg, OpRemove, "cowsay")
	if err != nil {
		t.Fatal(err)
	}
	res, err := run(ctx, cfg, OpRemove, p)
	if err != nil || res.Done != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if c := f.called(); !strings.HasPrefix(c[len(c)-1], "RemovePackages [2 ["+cowsay+"] true false]") {
		t.Fatalf("not applied with only-trusted: %v", c)
	}
	extra = true // something else would go now
	if _, err := run(ctx, cfg, OpRemove, p); status(err) != http.StatusConflict {
		t.Fatalf("changed plan ran: %v", err)
	}
	extra, authorized = false, false
	res, err = run(ctx, cfg, OpRemove, p)
	if err != nil || res.Done != 0 || len(res.Failed) != 1 || res.Failed[0].Reason != ReasonUnauthorized {
		t.Fatalf("%v %+v", err, res)
	}
}

// Unattended work never lets PackageKit ask for a password (S15d).
func TestUnattendedTransactionIsNotInteractive(t *testing.T) {
	f := &fakePK{script: func(string, []any) []sig { return nil }}
	c := &Client{Dial: func(context.Context) (Bus, error) { return f, nil }}
	_ = c.Apply(context.Background(), OpRemove, []string{"vlc;1;amd64;debian"})
	_ = c.Apply(unattended.With(context.Background()), OpRemove, []string{"vlc;1;amd64;debian"})
	if len(f.hints) != 2 || !slices.Contains(f.hints[0], "interactive=true") || !slices.Contains(f.hints[1], "interactive=false") {
		t.Fatalf("%v", f.hints)
	}
}
