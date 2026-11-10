package uninstall

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func local(t *testing.T) Local {
	h := t.TempDir()
	return Local{Home: h, UID: uint32(os.Getuid())} // #nosec G115 -- a test uid
}

func TestProgramByManager(t *testing.T) {
	l := local(t)
	cases := []struct {
		o     Owner
		group string
		kinds []string
	}{
		{Owner{Manager: ManagerApt, Target: "vlc", Packages: []string{"vlc:amd64", "vlc:i386"}}, GroupProgram, []string{KindPackage, KindPackage}},
		{Owner{Manager: ManagerFlatpak, Target: "system/app/x.y.Z/x86_64/stable"}, GroupProgram, []string{KindFlatpak}},
		{Owner{Manager: ManagerSnap, Target: "firefox"}, GroupProgram, []string{KindSnap}},
		{Owner{Manager: ManagerCargo, Target: "ripgrep"}, GroupProgram, []string{KindExtra}},
		{Owner{Manager: ManagerPip, Target: "black", Name: "black"}, GroupRemains, []string{KindManual}},
		{Owner{Manager: ManagerSteam, Target: "570"}, GroupProgram, []string{KindSteam}},
	}
	for _, c := range cases {
		got := l.Program(Resolved{Owner: c.o})
		var kinds []string
		for _, x := range got {
			kinds = append(kinds, x.Kind)
			if x.Group != c.group || x.Reason == "" {
				t.Errorf("%s: %+v", c.o.Manager, x)
			}
		}
		if !slices.Equal(kinds, c.kinds) {
			t.Errorf("%s: %v", c.o.Manager, kinds)
		}
	}
	if got := l.Program(Resolved{Owner: Owner{Manager: ManagerPip, Name: "black"}}); got[0].Name != "pip uninstall --user black" {
		t.Errorf("%+v", got)
	}
}

func TestManualAndAppImage(t *testing.T) {
	l := local(t)
	opt := filepath.Join(l.Home, ".local/opt/Foo")
	mkdir(t, opt)
	img := filepath.Join(l.Home, "Applications/Foo.AppImage")
	write(t, img, "x")
	mkdir(t, filepath.Join(l.Home, "Documents/x"))
	got := l.Program(Resolved{Owner: Owner{Manager: ManagerManual, Paths: []string{opt, filepath.Join(l.Home, "Documents/x"),
		"/etc/../x", "/usr/bin/ls", filepath.Join(l.Home, ".local/opt/gone")}}})
	want := []string{KindTrash, KindNothing, KindNothing, KindNothing, KindNothing}
	for i, c := range got {
		if c.Kind != want[i] {
			t.Errorf("%d: %+v", i, c)
		}
	}
	if !got[0].Preselect || got[0].Ino == 0 {
		t.Errorf("%+v", got[0])
	}
	got = l.Program(Resolved{Owner: Owner{Manager: ManagerAppImage, Paths: []string{img, "/tmp/Other.AppImage"}}})
	if got[0].Kind != KindTrash || !got[0].Preselect || got[1].Group != GroupRemains {
		t.Errorf("%+v", got)
	}
}

func TestGameDir(t *testing.T) {
	l := local(t)
	dirs := map[string]string{}
	l.InstallDir = func(id string) (string, bool) { d, ok := dirs[id]; return d, ok }
	game := func(id, name string) Candidate {
		return l.Program(Resolved{Owner: Owner{Manager: ManagerLutris, Target: id, Name: name}})[0]
	}
	mkdir(t, filepath.Join(l.Home, "Games/celeste"))
	mkdir(t, filepath.Join(l.Home, "Games/prefix-1"))
	mkdir(t, filepath.Join(l.Home, "Documents"))
	mkdir(t, filepath.Join(l.Home, ".config/x"))
	if err := os.Symlink(filepath.Join(l.Home, "Documents"), filepath.Join(l.Home, "Games/link")); err != nil {
		t.Fatal(err)
	}
	dirs["a"] = filepath.Join(l.Home, "Games/celeste")
	dirs["b"] = filepath.Join(l.Home, "Games/prefix-1")
	dirs["c"] = l.Home
	dirs["d"] = filepath.Join(l.Home, "Documents")
	dirs["e"] = filepath.Join(l.Home, ".config/x")
	dirs["f"] = filepath.Join(l.Home, "Games/link")
	dirs["g"] = "/srv/games/x"
	dirs["h"] = filepath.Join(l.Home, "Games/../Documents")
	if c := game("a", "Celeste"); c.Kind != KindTrash || !c.Preselect {
		t.Errorf("a: %+v", c)
	}
	if c := game("b", "Celeste"); c.Kind != KindTrash || c.Preselect {
		t.Errorf("b: %+v", c)
	}
	for _, id := range []string{"c", "d", "e", "f", "g", "h", "none"} {
		if c := game(id, "X"); c.Group != GroupRemains {
			t.Errorf("%s: %+v", id, c)
		}
	}
}

func TestSteamLeftovers(t *testing.T) {
	l := local(t)
	lib := filepath.Join(l.Home, ".local/share/Steam")
	l.SteamLibraries = func() []string { return []string{lib, "relative/lib"} }
	mkdir(t, filepath.Join(lib, "steamapps/compatdata/570"))
	mkdir(t, filepath.Join(lib, "steamapps/shadercache/570"))
	mkdir(t, filepath.Join(lib, "steamapps/compatdata/440"))
	got := l.SteamLeftovers("570")
	if len(got) != 2 || !got[0].Preselect || got[0].Group != GroupUser {
		t.Fatalf("%+v", got)
	}
	for _, bad := range []string{"", "../570", "57 0", "12345678901"} {
		if got := l.SteamLeftovers(bad); len(got) != 0 {
			t.Errorf("%q: %+v", bad, got)
		}
	}
}

func TestFlatpakFreed(t *testing.T) {
	list := discovery.ExtraList{Packages: []discovery.ExtraPackage{
		{ID: "system/app/org.gimp.GIMP/x86_64/stable", Name: "org.gimp.GIMP", Kind: "app"},
		{ID: "system/runtime/org.gnome.Platform/x86_64/48", Name: "org.gnome.Platform", Kind: "runtime", UsedBy: []string{"org.gimp.GIMP"}},
		{ID: "system/runtime/org.gnome.Platform.Locale/x86_64/48", Name: "org.gnome.Platform.Locale", Kind: "runtime", UsedBy: []string{"org.gimp.GIMP"}},
		{ID: "system/runtime/org.freedesktop.Platform/x86_64/24.08", Name: "org.freedesktop.Platform", Kind: "runtime", UsedBy: []string{"org.gimp.GIMP", "org.x.Y"}},
		{ID: "system/runtime/org.kde.Platform/x86_64/6.8", Name: "org.kde.Platform", Kind: "runtime"},
		{ID: "system/runtime/org.gnome.Platform/aarch64/48", Name: "org.gnome.Platform", Kind: "runtime", UsedBy: []string{"org.gimp.GIMP"}},
		{ID: "system/runtime/org.gimp.GIMP.Plugin.GMic/x86_64/2-40", Name: "org.gimp.GIMP.Plugin.GMic", Kind: "runtime"},
		{ID: "system/runtime/org.gimp.GIMP.Locale/x86_64/stable", Name: "org.gimp.GIMP.Locale", Kind: "runtime"},
	}}
	got := FlatpakFreed(list, "system/app/org.gimp.GIMP/x86_64/stable")
	var ids []string
	for _, c := range got {
		ids = append(ids, c.Name)
		if c.Preselect || c.Group != GroupOrphans {
			t.Errorf("%+v", c)
		}
	}
	if want := []string{"org.gnome.Platform", "org.gnome.Platform.Locale", "org.gimp.GIMP.Plugin.GMic"}; !slices.Equal(ids, want) {
		t.Fatalf("%v", ids)
	}
	if got := FlatpakFreed(list, "bad"); got != nil {
		t.Fatal(got)
	}
}

func TestAppImageIntegration(t *testing.T) {
	l := local(t)
	img := filepath.Join(l.Home, "Applications/Foo-1.2.AppImage")
	apps := filepath.Join(l.Home, ".local/share/applications")
	write(t, filepath.Join(apps, "appimagekit_abc-Foo.desktop"), "[Desktop Entry]\nName=Foo\nExec=\""+img+"\" %U\nIcon=appimagekit_abc_Foo\n[Desktop Action x]\nExec=/bin/other\n")
	write(t, filepath.Join(apps, "self.desktop"), "[Desktop Entry]\nExec="+img+"\nIcon=Foo-1.2\n")
	write(t, filepath.Join(apps, "shared.desktop"), "[Desktop Entry]\nExec="+img+" --x\nIcon=utilities-terminal\n")
	write(t, filepath.Join(apps, "other.desktop"), "[Desktop Entry]\nExec="+img+".old\nIcon=appimagekit_zzz\n")
	write(t, filepath.Join(apps, "action.desktop"), "[Desktop Entry]\nExec=/bin/x\n[Desktop Action a]\nExec="+img+"\n")
	if err := os.Symlink(filepath.Join(apps, "self.desktop"), filepath.Join(apps, "link.desktop")); err != nil {
		t.Fatal(err)
	}
	icons := filepath.Join(l.Home, ".local/share/icons/hicolor/256x256/apps")
	write(t, filepath.Join(icons, "appimagekit_abc_Foo.png"), "x")
	write(t, filepath.Join(icons, "Foo-1.2.svg"), "x")
	write(t, filepath.Join(icons, "utilities-terminal.png"), "x")
	write(t, filepath.Join(icons, "appimagekit_zzz.png"), "x")
	got := l.AppImageIntegration(img)
	pre := map[string]bool{}
	for _, c := range got {
		pre[filepath.Base(c.ID)] = c.Preselect
	}
	want := map[string]bool{"appimagekit_abc-Foo.desktop": true, "self.desktop": true, "shared.desktop": true,
		"appimagekit_abc_Foo.png": true, "Foo-1.2.svg": true, "utilities-terminal.png": false}
	if len(pre) != len(want) {
		t.Fatalf("%v", pre)
	}
	for k, v := range want {
		if p, ok := pre[k]; !ok || p != v {
			t.Errorf("%s: %v %v", k, p, ok)
		}
	}
}

// Whatever folder a launcher's record names, a folder offered for the
// trash is the user's, strictly inside the home at least two levels down,
// clean, not hidden at the top and not one of the home's own folders.
func FuzzGameDir(f *testing.F) {
	h := f.TempDir()
	for _, d := range []string{"Games/celeste", "Games", "Documents", ".config/x", "Games/a/b", ".local/share/x"} {
		if err := os.MkdirAll(filepath.Join(h, d), 0o700); err != nil {
			f.Fatal(err)
		}
	}
	l := Local{Home: h, UID: uint32(os.Getuid())} // #nosec G115 -- a test uid
	for _, s := range []string{"Games/celeste", "Games", "/Documents", "Games/../Documents", ".config/x", "Games/a/b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		dir := h + "/" + rel
		l.InstallDir = func(string) (string, bool) { return dir, true }
		c := l.Program(Resolved{Owner: Owner{Manager: ManagerHeroic, Target: "x", Name: "Celeste"}})[0]
		if c.Group == GroupRemains {
			return
		}
		r, ok := strings.CutPrefix(c.ID, h+"/")
		if !ok || filepath.Clean(c.ID) != c.ID || !strings.Contains(r, "/") || strings.HasPrefix(r, ".config") ||
			xdgDirs[filepath.Base(c.ID)] || c.Kind != KindTrash {
			t.Fatalf("offered %q", c.ID)
		}
	})
}
