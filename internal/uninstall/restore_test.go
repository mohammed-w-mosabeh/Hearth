package uninstall

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
	"github.com/mohammed-w-mosabeh/hearth/internal/uninstall/rules"
)

func TestPickVersion(t *testing.T) {
	avail := []string{"3.0.24-0+deb13u1", "3.0.23-0+deb13u1", "3.0.23-0+deb12u1"}
	for want, exp := range map[string][3]any{
		"3.0.23-0+deb13u1": {"3.0.23-0+deb13u1", true, true},
		"3.0.23-0+deb13u2": {"3.0.23-0+deb13u1", false, true},
		"2.0-1":            {"3.0.24-0+deb13u1", false, true},
	} {
		v, exact, ok := PickVersion(want, avail)
		if v != exp[0] || exact != exp[1] || ok != exp[2] {
			t.Errorf("%s: %s %v %v", want, v, exact, ok)
		}
	}
	if _, _, ok := PickVersion("1", nil); ok {
		t.Error("picked from nothing")
	}
}

func TestParseMadison(t *testing.T) {
	out := "       vlc | 3.0.24-0+deb13u1 | http://security.debian.org trixie-security/main amd64 Packages\n" +
		"       vlc | 3.0.23-0+deb13u1 | http://deb.debian.org trixie/main amd64 Packages\n" +
		"   libvlc5 | 3.0.24-0+deb13u1 | http://x amd64 Packages\n  junk line\n   -x | 1 | y\n"
	got := ParseMadison(out)
	if !slices.Equal(got["vlc"], []string{"3.0.24-0+deb13u1", "3.0.23-0+deb13u1"}) || len(got["libvlc5"]) != 1 || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

// Whatever apt-cache prints, a parsed name is a package name and a version
// has no space or bar, so it cannot change a PackageKit ID's fields.
func FuzzParseMadison(f *testing.F) {
	f.Add("  vlc | 1.0-1 | http://x amd64 Packages\n")
	f.Fuzz(func(t *testing.T, out string) {
		for name, vs := range ParseMadison(out) {
			if !rules.ValidPackage(name) {
				t.Fatalf("name %q", name)
			}
			for _, v := range vs {
				if v == "" || strings.ContainsAny(v, " \t|;\n/") || v[0] < '0' || v[0] > '9' {
					t.Fatalf("version %q", v)
				}
			}
		}
	})
}

type fakeRestoreRefs struct{ added []string }

func (f *fakeRestoreRefs) AddFavorite(_ context.Context, id string) error {
	f.added = append(f.added, "favorite "+id)
	return nil
}
func (f *fakeRestoreRefs) CreateAlertRule(_ context.Context, r store.AlertRule) (int64, error) {
	f.added = append(f.added, "rule "+r.Name)
	return 9, nil
}
func (f *fakeRestoreRefs) PutPlaytime(_ context.Context, p store.Playtime) error {
	f.added = append(f.added, "playtime "+p.ID)
	return nil
}
func (f *fakeRestoreRefs) AddModeRef(_ context.Context, m ModeRef) error {
	f.added = append(f.added, "mode "+m.Target)
	return nil
}

type restoreBroker struct{ calls []string }

func (b *restoreBroker) Call(_ context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	b.calls = append(b.calls, method+" "+string(raw))
	switch method {
	case "leftovers.list":
		return json.Unmarshal([]byte(`[{"op":"0123456789abcdef0123456789abcdef","item":"VLC","created":1,"entries":[
			{"path":"/etc/vlc","kind":"dir"},{"path":"/etc/vlc/vlcrc","kind":"conffile","package":"vlc:amd64"}]}]`), out)
	case "leftovers.restore":
		p := params.(broker.RestoreLeftoversParams)
		var res []map[string]any
		for _, path := range p.Paths {
			res = append(res, map[string]any{"path": path, "done": true})
		}
		b, _ := json.Marshal(map[string]any{"results": res})
		return json.Unmarshal(b, out)
	}
	return nil
}

type restorePK struct {
	installed [][]string
	refuse    bool
}

func (p *restorePK) Resolve(_ context.Context, _ bool, names []string) ([]pkgs.Change, error) {
	var out []pkgs.Change
	for _, n := range names {
		out = append(out, pkgs.Change{Name: n, Arch: "amd64", Version: "9", ID: n + ";9;amd64;debian"})
	}
	return out, nil
}
func (p *restorePK) Simulate(_ context.Context, _ pkgs.Op, ids []string) ([]pkgs.Change, error) {
	if p.refuse {
		return nil, &pkgs.TxError{Details: "version not found"}
	}
	return nil, nil
}
func (p *restorePK) Apply(_ context.Context, _ pkgs.Op, ids []string) error {
	p.installed = append(p.installed, ids)
	return nil
}

func TestRestore(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	home := t.TempDir()
	info := filepath.Join(home, ".local/share/Trash/info")
	mkdir(t, info)
	write(t, filepath.Join(info, "vlc.trashinfo"), "[Trash Info]\nPath=/x\n")
	rec := Record{ID: "0123456789abcdef0123456789abcde0", Item: "app:vlc", Name: "VLC", Created: time.Now().UnixMilli(), State: StateDone,
		BrokerOp: "0123456789abcdef0123456789abcdef",
		Undo: Undo{Packages: []PackageVersion{{ID: "vlc:amd64", Version: "3.0.23-0+deb13u1"}, {ID: "libgone:amd64", Version: "1"}},
			Trashed:   []Trashed{{Path: filepath.Join(home, ".config/vlc"), Name: "vlc", URI: "trash:///vlc"}, {Path: "/mnt/x", Name: ""}},
			Archived:  []string{"/nonexistent-hearth-test/etc-vlc"},
			Favorites: []string{"vlc"}, Rules: []store.AlertRule{{ID: 3, Name: "VLC"}}, Others: []string{"npm:/p::x"},
			Modes: []ModeRef{{Mode: "evening", Kind: "open", Target: "vlc"}}}}
	raw, _ := json.Marshal(rec)
	if err := db.SaveUninstallOp(ctx, store.UninstallOp{ID: rec.ID, Item: rec.Item, Name: rec.Name, Created: rec.Created, Updated: rec.Created, State: rec.State, Record: raw}); err != nil {
		t.Fatal(err)
	}
	b := &restoreBroker{}
	pk := &restorePK{}
	refs := &fakeRestoreRefs{}
	var ran []string
	r := &Restorer{DB: db, PK: pk, Broker: b, Home: home, Refs: refs,
		Madison: func(context.Context, ...string) (string, error) {
			return "  vlc | 3.0.24-0+deb13u1 | x\n  vlc | 3.0.23-0+deb13u1 | x\n", nil
		},
		Run: func(_ context.Context, _, prog string, argv, _ []string) (string, error) {
			ran = append(ran, strings.Join(argv, " "))
			return "", nil
		}}
	ap, err := r.plan(ctx, actions.Request{Target: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	detail := map[string]string{}
	for _, it := range ap.Items {
		detail[it.ID] = it.Detail
	}
	if detail["package|vlc:amd64"] != "version 3.0.23-0+deb13u1" {
		t.Fatalf("%v", detail)
	}
	blocked := map[string]string{}
	for _, b := range ap.Blocked {
		blocked[b.ID] = b.Detail
	}
	if !strings.Contains(blocked["package|libgone:amd64"], "no longer") || !strings.Contains(blocked["trash|/mnt/x"], "another disk") ||
		!strings.Contains(blocked["manual|npm:/p::x"], "yourself") {
		t.Fatalf("%v", blocked)
	}
	res, err := r.run(ctx, actions.Request{Target: rec.ID}, ap)
	if err != nil || len(res.Failed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(pk.installed) != 1 || !slices.Equal(pk.installed[0], []string{"vlc;3.0.23-0+deb13u1;amd64;"}) {
		t.Fatalf("%v", pk.installed)
	}
	if !slices.Equal(ran, []string{"gio trash --restore -- trash:///vlc"}) {
		t.Fatalf("%v", ran)
	}
	// Archived folders first, configuration after the packages.
	var methods []string
	for _, c := range b.calls {
		methods = append(methods, c[:strings.IndexByte(c, ' ')])
	}
	if !slices.Equal(methods, []string{"leftovers.list", "leftovers.restore", "leftovers.restore"}) || !strings.Contains(b.calls[2], "vlcrc") {
		t.Fatalf("%v", b.calls)
	}
	if len(refs.added) != 3 {
		t.Fatalf("%v", refs.added)
	}
	op, _, _ := db.GetUninstallOp(ctx, rec.ID)
	if op.State != StateRestored {
		t.Fatalf("state %s", op.State)
	}
	if _, err := r.plan(ctx, actions.Request{Target: rec.ID}); err == nil {
		t.Fatal("restored twice")
	}

	// A version PackageKit cannot install falls back to apt's, and says so.
	pk2 := &restorePK{refuse: true}
	rows := []RestoreRow{{Kind: "package", ID: "vlc:amd64", Name: "vlc", OK: true, Pkg: "vlc;1;amd64;"}}
	r.PK = pk2
	r.installPackages(ctx, rows, []int{0})
	if !rows[0].Done || !strings.Contains(rows[0].Detail, "apt's: 9") || pk2.installed[0][0] != "vlc;9;amd64;debian" {
		t.Fatalf("%+v %v", rows, pk2.installed)
	}
}

func TestRestoreRefusesOld(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := Record{ID: "0123456789abcdef0123456789abcde1", Created: time.Now().Add(-8 * 24 * time.Hour).UnixMilli(), State: StateDone}
	raw, _ := json.Marshal(rec)
	_ = db.SaveUninstallOp(context.Background(), store.UninstallOp{ID: rec.ID, Created: rec.Created, Updated: rec.Created, State: rec.State, Record: raw})
	r := &Restorer{DB: db}
	for _, id := range []string{rec.ID, "nope", "0123456789abcdef0123456789abcde2"} {
		if _, err := r.plan(context.Background(), actions.Request{Target: id}); err == nil {
			t.Errorf("%s: planned", id)
		}
	}
}
