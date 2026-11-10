package uninstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type fakeBroker struct {
	calls []string
	fail  string
	auth  broker.UninstallAuthorizeParams
}

func (b *fakeBroker) Call(_ context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	b.calls = append(b.calls, method+" "+string(raw))
	if method == b.fail {
		return errors.New(method + " failed")
	}
	switch method {
	case "uninstall.authorize":
		b.auth = params.(broker.UninstallAuthorizeParams)
		return json.Unmarshal([]byte(`{"op":"0123456789abcdef0123456789abcdef"}`), out)
	case "pkg.purge":
		if out != nil {
			return json.Unmarshal([]byte(`{"purged":[],"left":[]}`), out)
		}
	}
	return nil
}

type fakePK struct {
	removes map[string][]pkgs.Change // simulation by first id
	applied [][]string
	fail    bool
}

func (f *fakePK) Resolve(_ context.Context, _ bool, names []string) ([]pkgs.Change, error) {
	var out []pkgs.Change
	for _, n := range names {
		out = append(out, pkgs.Change{Name: n, Arch: "amd64", ID: n + ";1;amd64;installed"})
	}
	return out, nil
}

func (f *fakePK) Simulate(_ context.Context, _ pkgs.Op, ids []string) ([]pkgs.Change, error) {
	return f.removes[ids[0]], nil
}

func (f *fakePK) Apply(_ context.Context, _ pkgs.Op, ids []string) error {
	if f.fail {
		return errors.New("PackageKit failed")
	}
	f.applied = append(f.applied, ids)
	return nil
}

type fakeRefs struct{ removed []string }

func (f *fakeRefs) RemoveFavorite(_ context.Context, id string) error {
	f.removed = append(f.removed, "favorite "+id)
	return nil
}
func (f *fakeRefs) DeleteAlertRule(_ context.Context, id int64) (store.AlertRule, error) {
	return store.AlertRule{ID: id, Name: "r"}, nil
}
func (f *fakeRefs) TakePlaytime(_ context.Context, id string) (store.Playtime, bool, error) {
	return store.Playtime{ID: id, Seconds: 5}, true, nil
}
func (f *fakeRefs) RemoveModeRef(_ context.Context, mode, kind, target string) (string, error) {
	return "start", nil
}

func removing(names ...string) []pkgs.Change {
	var out []pkgs.Change
	for _, n := range names {
		out = append(out, pkgs.Change{Name: n, Arch: "amd64", Version: "1.0", Info: "removing"})
	}
	return out
}

type engineRig struct {
	e      *Engine
	b      *fakeBroker
	pk     *fakePK
	refs   *fakeRefs
	saved  []Record
	killed []string
	home   string
	alive  map[int]bool
}

func newEngineRig(t *testing.T) *engineRig {
	g := &engineRig{b: &fakeBroker{}, refs: &fakeRefs{}, home: t.TempDir(), alive: map[int]bool{10: true},
		pk: &fakePK{removes: map[string][]pkgs.Change{
			"vlc;1;amd64;installed":     removing("vlc", "vlc-plugin-x"),
			"libvlc5;1;amd64;installed": removing("libvlc5"),
		}}}
	g.e = &Engine{Broker: g.b, PK: g.pk, Refs: g.refs, Grace: 1,
		Save:     func(_ context.Context, r Record) error { g.saved = append(g.saved, r); return nil },
		Extra:    func(_ context.Context, kind, id string) error { return nil },
		UserUnit: func(_ context.Context, name string) error { return nil },
		Open:     func(_ context.Context, url string) error { return nil },
		Alive:    func(pid int, _ uint64) bool { return g.alive[pid] },
		Signal: func(pid int, _ uint64, sig syscall.Signal) error {
			g.killed = append(g.killed, sig.String())
			g.alive[pid] = false
			return nil
		},
		Trash: Trasher{Home: g.home, Run: fakeGio(t, g.home, 0)},
	}
	return g
}

func (g *engineRig) userDir(t *testing.T, rel string) Candidate {
	p := filepath.Join(g.home, rel)
	mkdir(t, p)
	c, _ := pathCandidate(GroupUser, KindTrash, p, "x", true)
	return c
}

func vlcPlan(t *testing.T, g *engineRig) Plan {
	return Plan{Item: "app:vlc", Name: "VLC", Owner: Owner{Manager: ManagerApt, Packages: []string{"vlc:amd64"}}, Candidates: []Candidate{
		{Group: GroupProgram, Kind: KindPackage, ID: "vlc:amd64", Name: "vlc", Preselect: true, Size: 100},
		{Group: GroupProgram, Kind: KindPackage, ID: "vlc-plugin-x:amd64", Name: "vlc-plugin-x", Preselect: true},
		{Group: GroupOrphans, Kind: KindPackage, ID: "libvlc5:amd64", Name: "libvlc5", Preselect: true, Size: 50},
		{Group: GroupPurge, Kind: KindPackage, ID: "vlc:amd64", Name: "vlc", Preselect: true},
		{Group: GroupPurge, Kind: KindPackage, ID: "vlc-plugin-x:amd64", Name: "vlc-plugin-x", Preselect: true},
		{Group: GroupPurge, Kind: KindPackage, ID: "libvlc5:amd64", Name: "libvlc5", Preselect: true},
		{Group: GroupRunning, Kind: KindProcess, ID: "pid:10:5", Name: "vlc", Preselect: true},
		{Group: GroupRunning, Kind: KindUnit, ID: "system/vlc.service", Name: "vlc.service", Preselect: true},
		{Group: GroupRunning, Kind: KindUnit, ID: "user/vlc-tray.service", Name: "vlc-tray.service", Preselect: true},
		g.userDir(t, ".config/vlc"),
		{Group: GroupSystem, Kind: KindArchive, ID: "/etc/vlc", Name: "/etc/vlc", Dev: 1, Ino: 2},
		{Group: GroupRefs, Kind: KindRef, ID: "favorite:vlc", Name: "vlc", Preselect: true},
		{Group: GroupRefs, Kind: KindRef, ID: "mode:evening:open:vlc", Name: "Evening: vlc"},
		{Group: GroupRemains, Kind: KindNothing, ID: "libmine:amd64", Name: "libmine"},
	}}
}

// all selects every row but the made-up system leftover, which the
// revalidation would refuse (it is not on this machine).
func all(p Plan) map[string]bool {
	sel := map[string]bool{}
	for _, c := range p.Candidates {
		if c.ID != "/etc/vlc" {
			sel[c.Key()] = true
		}
	}
	return sel
}

func TestEngineRunsInOrder(t *testing.T) {
	g := newEngineRig(t)
	p := vlcPlan(t, g)
	rows := Selected(p, all(p)) // the system leftover is left unchecked
	rec, err := g.e.Run(context.Background(), p, rows)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateDone || rec.BrokerOp == "" || rec.Freed != 150 {
		t.Fatalf("%+v", rec)
	}
	var methods []string
	for _, c := range g.b.calls {
		m, _, _ := strings.Cut(c, " ")
		methods = append(methods, m)
	}
	want := []string{"uninstall.authorize", "unit.disable", "leftovers.archive", "pkg.purge", "leftovers.archive", "pkg.purge"}
	if !slices.Equal(methods, want) {
		t.Fatalf("broker calls %v", methods)
	}
	// One authorization holds every broker step, and nothing unchecked.
	if !slices.Equal(g.b.auth.Units, []string{"vlc.service"}) || len(g.b.auth.Archive) != 0 ||
		!slices.Equal(g.b.auth.Purge, []string{"vlc:amd64", "vlc-plugin-x:amd64", "libvlc5:amd64"}) {
		t.Fatalf("%+v", g.b.auth)
	}
	if len(g.pk.applied) != 2 || g.pk.applied[0][0] != "vlc;1;amd64;installed" || g.pk.applied[1][0] != "libvlc5;1;amd64;installed" {
		t.Fatalf("%v", g.pk.applied)
	}
	if !slices.Equal(g.killed, []string{"terminated"}) {
		t.Fatalf("signals %v", g.killed)
	}
	if len(rec.Undo.Packages) != 3 || len(rec.Undo.Trashed) != 1 || !slices.Equal(rec.Undo.Favorites, []string{"vlc"}) ||
		len(rec.Undo.Modes) != 1 || rec.Undo.Modes[0].Do != "start" {
		t.Fatalf("%+v", rec.Undo)
	}
	if _, err := os.Lstat(filepath.Join(g.home, ".config/vlc")); !os.IsNotExist(err) {
		t.Fatal("the user leftover is still there")
	}
	if g.saved[len(g.saved)-1].State != StateDone || g.saved[0].State != StateRunning {
		t.Fatal("the record was not kept as the run went")
	}
}

func TestEngineStopsAtFirstFailure(t *testing.T) {
	g := newEngineRig(t)
	g.pk.fail = true
	p := vlcPlan(t, g)
	rec, err := g.e.Run(context.Background(), p, Selected(p, all(p)))
	if !errors.Is(err, ErrStopped) || rec.State != StateFailed || rec.Failed != StepProgram {
		t.Fatalf("%v %+v", err, rec)
	}
	for _, c := range g.b.calls {
		if strings.HasPrefix(c, "pkg.purge") || strings.HasPrefix(c, "leftovers.archive") {
			t.Fatalf("ran past the failure: %s", c)
		}
	}
	if _, err := os.Lstat(filepath.Join(g.home, ".config/vlc")); err != nil {
		t.Fatal("a leftover after the failure was touched")
	}
	if len(g.refs.removed) != 0 {
		t.Fatal("references were removed after the failure")
	}
	failed := 0
	for _, s := range rec.Steps {
		if !s.Done {
			failed++
		}
	}
	if failed != 1 || !strings.Contains(rec.Steps[len(rec.Steps)-1].Detail, "PackageKit failed") {
		t.Fatalf("%+v", rec.Steps)
	}
}

func TestEngineRefusesUnexpectedRemovals(t *testing.T) {
	g := newEngineRig(t)
	g.pk.removes["vlc;1;amd64;installed"] = removing("vlc", "vlc-plugin-x", "libc6")
	p := vlcPlan(t, g)
	rec, err := g.e.Run(context.Background(), p, Selected(p, all(p)))
	if err == nil || rec.Failed != StepProgram || len(g.pk.applied) != 0 {
		t.Fatalf("%v %+v %v", err, rec, g.pk.applied)
	}
}

func TestEngineRevalidatesFirst(t *testing.T) {
	g := newEngineRig(t)
	p := vlcPlan(t, g)
	// The leftover becomes a link to /etc between preview and run.
	dir := filepath.Join(g.home, ".config/vlc")
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", dir); err != nil {
		t.Fatal(err)
	}
	rec, err := g.e.Run(context.Background(), p, Selected(p, all(p)))
	if err == nil || rec.Failed != StepRevalidate {
		t.Fatalf("%v %+v", err, rec)
	}
	if len(g.b.calls) != 0 || len(g.pk.applied) != 0 || len(g.killed) != 0 {
		t.Fatal("something ran before the revalidation")
	}
}

func TestEngineBlockedPlan(t *testing.T) {
	g := newEngineRig(t)
	p := vlcPlan(t, g)
	p.Blocked = "bash can never be removed (essential)"
	if _, err := g.e.Run(context.Background(), p, Selected(p, all(p))); err == nil || len(g.b.calls) != 0 {
		t.Fatalf("a blocked plan ran: %v", err)
	}
}

func TestEngineNoBrokerWhenNotNeeded(t *testing.T) {
	g := newEngineRig(t)
	p := Plan{Item: "flatpak:x", Name: "X", Owner: Owner{Manager: ManagerFlatpak}, Candidates: []Candidate{
		{Group: GroupProgram, Kind: KindFlatpak, ID: "system/app/org.x.Y/x86_64/stable", Name: "X", Preselect: true},
		g.userDir(t, ".var/app/org.x.Y"),
	}}
	var removed []string
	g.e.Extra = func(_ context.Context, kind, id string) error { removed = append(removed, kind+" "+id); return nil }
	rec, err := g.e.Run(context.Background(), p, Selected(p, all(p)))
	if err != nil || len(g.b.calls) != 0 || len(removed) != 1 || len(rec.Undo.Flatpaks) != 1 {
		t.Fatalf("%v %v %v %+v", err, g.b.calls, removed, rec.Undo)
	}
}
