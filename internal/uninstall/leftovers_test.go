package uninstall

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func TestUserLeftovers(t *testing.T) {
	l := local(t)
	h := l.Home
	for _, d := range []string{
		".config/vlc", ".config/org.videolan.VLC", ".local/share/vlc", ".cache/vlc", ".var/app/org.videolan.VLC",
		".config/autostart", ".config/chromium", ".config/hearth", ".vlc", ".config/data", ".config/shared",
		".local/state/vlc", "snap/vlc", "Documents/vlc",
	} {
		mkdir(t, filepath.Join(h, d))
	}
	write(t, filepath.Join(h, ".config/vlc/vlcrc"), strings.Repeat("x", 5000))
	if err := os.Symlink("/etc", filepath.Join(h, ".cache/VLC")); err != nil {
		t.Fatal(err)
	}
	ids := Identifiers{Packages: []string{"vlc", "data"}, AppIDs: []string{"org.videolan.VLC"}, Exec: []string{"chromium", "shared"},
		Snaps: []string{"vlc"}}
	s := Scan{Home: h, UID: l.UID,
		Others: Identifiers{Packages: []string{"chromium-common"}, Desktop: []string{"chromium"}},
		Owned: func(paths []string) (map[string]string, error) {
			return map[string]string{filepath.Join(h, ".config/shared"): "shared-data"}, nil
		}}
	got := s.UserLeftovers(context.Background(), ids, map[string]bool{filepath.Join(h, ".local/state/vlc"): true})
	byID := map[string]Candidate{}
	for _, c := range got {
		byID[strings.TrimPrefix(c.ID, h+"/")] = c
	}
	want := map[string]struct {
		group string
		pre   bool
	}{
		".config/vlc":               {GroupUser, false},
		".config/org.videolan.VLC":  {GroupUser, true},
		".local/share/vlc":          {GroupUser, false},
		".cache/vlc":                {GroupUser, false},
		".cache/VLC":                {GroupUser, false}, // the link only
		".var/app/org.videolan.VLC": {GroupUser, true},
		".vlc":                      {GroupUser, false},
		"snap/vlc":                  {GroupUser, true},
		".config/chromium":          {GroupRemains, false}, // named like an app that stays
		".config/shared":            {GroupRemains, false}, // owned by a package
	}
	for k, w := range want {
		c, ok := byID[k]
		if !ok || c.Group != w.group || c.Preselect != w.pre {
			t.Errorf("%s: %+v %v", k, c, ok)
		}
	}
	if len(byID) != len(want) {
		t.Errorf("got %d: %v", len(byID), byID)
	}
	if byID[".config/vlc"].Size < 5000 {
		t.Errorf("size %d", byID[".config/vlc"].Size)
	}
	if !strings.Contains(byID[".cache/VLC"].Reason, "only the link") {
		t.Error(byID[".cache/VLC"].Reason)
	}
}

// fakeGio moves files into a fake home trash, writing info files as gio
// does, and fails after stopAfter files when set.
func fakeGio(t *testing.T, home string, stopAfter int) RunFunc {
	return func(_ context.Context, _, prog string, argv, _ []string) (string, error) {
		if prog != "/usr/bin/gio" || len(argv) < 4 || argv[1] != "trash" || argv[2] != "--" {
			t.Fatalf("ran %s %v", prog, argv)
		}
		files := filepath.Join(home, ".local/share/Trash/files")
		info := filepath.Join(home, ".local/share/Trash/info")
		mkdir(t, files)
		mkdir(t, info)
		for i, p := range argv[3:] {
			if stopAfter > 0 && i >= stopAfter {
				return "", errors.New("gio: stopped")
			}
			name := filepath.Base(p)
			if _, err := os.Lstat(filepath.Join(files, name)); err == nil {
				name += ".2"
			}
			if err := os.Rename(p, filepath.Join(files, name)); err != nil {
				return "", err
			}
			write(t, filepath.Join(info, name+".trashinfo"), "[Trash Info]\nPath="+(&url.URL{Path: p}).EscapedPath()+"\nDeletionDate=2026-11-10T10:00:00\n")
		}
		return "", nil
	}
}

func TestTrash(t *testing.T) {
	l := local(t)
	var cs []Candidate
	for _, n := range []string{"a b", "c", "c2"} {
		p := filepath.Join(l.Home, ".config", n)
		mkdir(t, p)
		c, _ := pathCandidate(GroupUser, KindTrash, p, "x", true)
		cs = append(cs, c)
	}
	// An older trash entry for the same original path stays untouched.
	write(t, filepath.Join(l.Home, ".local/share/Trash/info/old.trashinfo"), "[Trash Info]\nPath="+cs[1].ID+"\n")
	tr := Trasher{Home: l.Home, Run: fakeGio(t, l.Home, 0)}
	done, err := tr.Trash(context.Background(), cs)
	if err != nil || len(done) != 3 {
		t.Fatalf("%+v %v", done, err)
	}
	if done[0].Path != cs[0].ID || done[0].Name != "a b" || done[0].URI != "trash:///a%20b" {
		t.Errorf("%+v", done[0])
	}
}

func TestTrashRefusesSwap(t *testing.T) {
	l := local(t)
	p := filepath.Join(l.Home, ".config/vlc")
	mkdir(t, p)
	c, _ := pathCandidate(GroupUser, KindTrash, p, "x", true)
	q := filepath.Join(l.Home, ".config/other")
	mkdir(t, q)
	c2, _ := pathCandidate(GroupUser, KindTrash, q, "x", true)
	// Between preview and run, vlc becomes a link to /etc.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", p); err != nil {
		t.Fatal(err)
	}
	ran := false
	tr := Trasher{Home: l.Home, Run: func(context.Context, string, string, []string, []string) (string, error) {
		ran = true
		return "", nil
	}}
	done, err := tr.Trash(context.Background(), []Candidate{c2, c})
	if !errors.Is(err, ErrChanged) || ran || len(done) != 0 {
		t.Fatalf("%+v %v ran=%v", done, err, ran)
	}
	if _, err := os.Lstat(q); err != nil {
		t.Fatal("a path before the swapped one in the same batch was touched")
	}
}

func TestTrashStopsAndReports(t *testing.T) {
	l := local(t)
	var cs []Candidate
	for i := range 20 {
		p := filepath.Join(l.Home, ".config", "d"+string(rune('a'+i)))
		mkdir(t, p)
		c, _ := pathCandidate(GroupUser, KindTrash, p, "x", true)
		cs = append(cs, c)
	}
	// gio stops after 3 files of the first batch of 16.
	tr := Trasher{Home: l.Home, Run: fakeGio(t, l.Home, 3)}
	done, err := tr.Trash(context.Background(), cs)
	if err == nil || len(done) != 3 {
		t.Fatalf("%d done, %v", len(done), err)
	}
	for _, c := range cs[16:] {
		if _, err := os.Lstat(c.ID); err != nil {
			t.Fatalf("%s was touched after the failure", c.ID)
		}
	}
}

func TestRefs(t *testing.T) {
	res := Resolved{Item: "app:vlc", Owner: Owner{Manager: ManagerApt}, Identifiers: Identifiers{Desktop: []string{"vlc"}}}
	d := RefData{
		Favorites: []string{"firefox", "vlc"},
		Rules: []store.AlertRule{{ID: 3, Name: "VLC daemon", Metric: "unit.failed", Target: "vlc-daemon.service"},
			{ID: 4, Name: "Disk", Metric: "disk.used_pct", Target: "vlc-daemon.service"}},
		Playtime: map[string]int64{"desktop.vlc": 30, "steam.570": 9},
		Modes: []modes.Mode{{ID: "evening", Name: "Evening",
			Items:   []modes.Item{{Kind: modes.KindApp, Target: "vlc", Do: "start"}, {Kind: modes.KindUnit, Target: "system/vlc-daemon.service", Do: "stop"}, {Kind: modes.KindUnit, Target: "user/x.service", Do: "stop"}},
			Session: modes.Session{Open: []string{"vlc", "firefox"}}}},
	}
	got := Refs(d, res, []string{"system/vlc-daemon.service"})
	want := map[string]bool{"favorite:vlc": true, "alert:3": true, "playtime:desktop.vlc": true,
		"mode:evening:app:vlc": false, "mode:evening:unit:system/vlc-daemon.service": false, "mode:evening:open:vlc": false}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for _, c := range got {
		pre, ok := want[c.ID]
		if !ok || pre != c.Preselect || c.Group != GroupRefs {
			t.Errorf("%+v", c)
		}
	}
	steam := Refs(d, Resolved{Item: "game:steam.570", Owner: Owner{Manager: ManagerSteam, Name: "Dota"}}, nil)
	if len(steam) != 1 || steam[0].ID != "playtime:steam.570" {
		t.Fatalf("%+v", steam)
	}
}

// An inode number can be reused at once: a link put where a folder was may
// carry the folder's device and inode. Its type still differs.
func TestRecheckComparesType(t *testing.T) {
	l := local(t)
	link := filepath.Join(l.Home, ".config/vlc")
	mkdir(t, filepath.Dir(link))
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	c, _ := pathCandidate(GroupUser, KindTrash, link, "x", true)
	if err := Recheck(c); err != nil {
		t.Fatal(err)
	}
	c.Type = os.ModeDir // as previewed: a folder with this inode
	if err := Recheck(c); !errors.Is(err, ErrChanged) {
		t.Fatalf("%v", err)
	}
}
