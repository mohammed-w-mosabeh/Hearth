package uninstall

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
)

type fakeApps map[string]discovery.App

func (f fakeApps) App(id string) (discovery.App, bool) { a, ok := f[id]; return a, ok }

type fakeTools map[string]discovery.Tool

func (f fakeTools) Find(_ context.Context, n string) (discovery.Tool, bool, error) {
	t, ok := f[n]
	return t, ok, nil
}

type fakeGames struct {
	games map[string]discovery.Game
	apps  map[string]string
}

func (f fakeGames) Game(id string) (discovery.Game, bool) { g, ok := f.games[id]; return g, ok }
func (f fakeGames) LaunchTarget(id string) (string, string, string, bool) {
	g, ok := f.games[id]
	return g.Name, f.apps[id], "", ok
}

type fakeDpkg struct {
	pkgs   []discovery.Package
	files  map[string][]string // id -> files
	owners map[string]string
}

func (f fakeDpkg) ByName(n string) ([]discovery.Package, error) {
	var out []discovery.Package
	for _, p := range f.pkgs {
		if p.Name == n {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f fakeDpkg) Detail(id string) (discovery.PackageDetail, bool, error) {
	for _, p := range f.pkgs {
		if p.ID() == id {
			return discovery.PackageDetail{Package: p, Files: f.files[id]}, true, nil
		}
	}
	return discovery.PackageDetail{}, false, nil
}

func (f fakeDpkg) Owners(paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range paths {
		if o, ok := f.owners[p]; ok {
			out[p] = o
		}
	}
	return out, nil
}

const home = "/home/u"

func rig() *Resolver {
	return &Resolver{
		Home: home,
		Apps: fakeApps{
			"vlc":             {ID: "vlc", Name: "VLC", Source: discovery.SourceSystem, Path: "/usr/share/applications/vlc.desktop", Exec: "/usr/bin/vlc --started-from-file %U", WMClass: "vlc"},
			"org.gnome.Maps":  {ID: "org.gnome.Maps", Name: "Maps", Source: discovery.SourceFlatpak, FlatpakID: "org.gnome.Maps", Exec: "/usr/bin/flatpak run org.gnome.Maps"},
			"firefox_firefox": {ID: "firefox_firefox", Name: "Firefox", Source: discovery.SourceSnap, Exec: "/snap/bin/firefox"},
			"appimage.Foo":    {ID: "appimage.Foo", Name: "Foo", Source: discovery.SourceAppImage, Path: home + "/Applications/Foo-1.2.AppImage"},
			"obsidian":        {ID: "obsidian", Name: "Obsidian", Source: discovery.SourceUser, Path: home + "/.local/share/applications/obsidian.desktop", Exec: "/opt/Obsidian/obsidian %U"},
			"bar":             {ID: "bar", Name: "Bar", Source: discovery.SourceUser, Path: home + "/.local/share/applications/bar.desktop", Exec: "\"" + home + "/Apps/Bar.AppImage\" --x"},
			"lost":            {ID: "lost", Name: "Lost", Source: discovery.SourceUser, Path: home + "/.local/share/applications/lost.desktop", Exec: "lost"},
			"org.gnome.Calc":  {ID: "org.gnome.Calc", Name: "Calc", Source: discovery.SourceSystem, Path: "/usr/share/applications/org.gnome.Calc.desktop", Exec: "env GDK=x gnome-calculator", WMClass: "gnome-calculator"},
		},
		Tools: fakeTools{
			"vlc":  {Name: "vlc", Path: "/usr/bin/vlc", Manager: "apt", Package: "vlc", PackageID: "vlc"},
			"rg":   {Name: "rg", Path: home + "/.cargo/bin/rg", Manager: "cargo", PackageID: "ripgrep"},
			"obs":  {Name: "obs", Path: "/usr/local/bin/obs", Target: "/opt/Obsidian/obsidian"},
			"mine": {Name: "mine", Path: "/usr/local/bin/mine"},
			"odd":  {Name: "odd", Path: home + "/bin/odd"},
		},
		Games: fakeGames{
			games: map[string]discovery.Game{
				"steam.570":   {ID: "steam.570", Name: "Dota", Source: discovery.GameSteam},
				"desktop.vlc": {ID: "desktop.vlc", Name: "VLC game", Source: discovery.GameDesktop},
				"lutris.3":    {ID: "lutris.3", Name: "L", Source: discovery.GameLutris},
			},
			apps: map[string]string{"desktop.vlc": "vlc"},
		},
		Packages: fakeDpkg{
			pkgs: []discovery.Package{
				{Name: "vlc", Arch: "amd64", State: "installed"},
				{Name: "gnome-calculator", Arch: "amd64", State: "installed"},
				{Name: "gone", Arch: "amd64", State: "config-files"},
			},
			files: map[string][]string{
				"vlc:amd64":              {"/usr/bin/vlc", "/usr/bin/cvlc", "/usr/share/applications/vlc.desktop", "/usr/lib/vlc/x.so"},
				"gnome-calculator:amd64": {"/usr/bin/gnome-calculator", "/usr/share/applications/org.gnome.Calc.desktop"},
			},
			owners: map[string]string{
				"/usr/share/applications/vlc.desktop":            "vlc",
				"/usr/share/applications/org.gnome.Calc.desktop": "gnome-calculator",
			},
		},
		Extra: func(_ context.Context, m string) discovery.ExtraList {
			switch m {
			case ManagerFlatpak:
				return discovery.ExtraList{Packages: []discovery.ExtraPackage{
					{ID: "system/runtime/org.gnome.Platform/x86_64/48", Name: "org.gnome.Platform", Kind: "runtime"},
					{ID: "system/app/org.gnome.Maps/x86_64/stable", Name: "org.gnome.Maps", Kind: "app"},
				}}
			case ManagerSnap:
				return discovery.ExtraList{Packages: []discovery.ExtraPackage{{ID: "firefox", Name: "firefox"}}}
			case ManagerCargo:
				return discovery.ExtraList{Packages: []discovery.ExtraPackage{{ID: "ripgrep", Name: "ripgrep", Bins: []string{"rg"}}}}
			}
			return discovery.ExtraList{}
		},
	}
}

func TestResolve(t *testing.T) {
	r := rig()
	ctx := context.Background()
	cases := []struct {
		item, manager, target string
		check                 func(Resolved) bool
	}{
		{"app:vlc", ManagerApt, "vlc", func(x Resolved) bool {
			return slices.Equal(x.Owner.Packages, []string{"vlc:amd64"}) && slices.Contains(x.Identifiers.Exec, "cvlc") &&
				slices.Contains(x.Identifiers.Desktop, "vlc") && slices.Contains(x.Identifiers.WMClass, "vlc")
		}},
		{"app:org.gnome.Maps", ManagerFlatpak, "system/app/org.gnome.Maps/x86_64/stable", func(x Resolved) bool {
			return slices.Equal(x.Identifiers.AppIDs, []string{"org.gnome.Maps"}) && !slices.Contains(x.Identifiers.Exec, "flatpak")
		}},
		{"app:firefox_firefox", ManagerSnap, "firefox", func(x Resolved) bool { return slices.Equal(x.Identifiers.Snaps, []string{"firefox"}) }},
		{"app:appimage.Foo", ManagerAppImage, home + "/Applications/Foo-1.2.AppImage", func(x Resolved) bool {
			return slices.Equal(x.Owner.Paths, []string{home + "/Applications/Foo-1.2.AppImage"})
		}},
		{"app:obsidian", ManagerManual, "/opt/Obsidian", func(x Resolved) bool { return slices.Contains(x.Identifiers.Names, "Obsidian") }},
		{"app:bar", ManagerAppImage, home + "/Apps/Bar.AppImage", nil},
		{"app:org.gnome.Calc", ManagerApt, "gnome-calculator", func(x Resolved) bool {
			return slices.Contains(x.Identifiers.AppIDs, "org.gnome.Calc") && slices.Contains(x.Identifiers.Exec, "gnome-calculator")
		}},
		{"tool:vlc", ManagerApt, "vlc", nil},
		{"tool:rg", ManagerCargo, "ripgrep", func(x Resolved) bool { return slices.Contains(x.Identifiers.Exec, "rg") }},
		{"tool:obs", ManagerManual, "/opt/Obsidian", func(x Resolved) bool {
			return slices.Equal(x.Owner.Paths, []string{"/opt/Obsidian", "/usr/local/bin/obs"})
		}},
		{"tool:mine", ManagerManual, "/usr/local/bin/mine", nil},
		{"package:vlc", ManagerApt, "vlc", nil},
		{"package:vlc:amd64", ManagerApt, "vlc:amd64", nil},
		{"flatpak:system/app/org.gnome.Maps/x86_64/stable", ManagerFlatpak, "system/app/org.gnome.Maps/x86_64/stable", nil},
		{"snap:firefox", ManagerSnap, "firefox", nil},
		{"game:steam.570", ManagerSteam, "570", func(x Resolved) bool { return len(x.Identifiers.Names) == 0 }},
		{"game:desktop.vlc", ManagerApt, "vlc", nil},
		{"game:lutris.3", ManagerLutris, "lutris.3", nil},
	}
	for _, c := range cases {
		res, err := r.Resolve(ctx, c.item)
		if err != nil {
			t.Errorf("%s: %v", c.item, err)
			continue
		}
		if res.Item != c.item || res.Owner.Manager != c.manager || res.Owner.Target != c.target {
			t.Errorf("%s: %+v", c.item, res.Owner)
		}
		if c.check != nil && !c.check(res) {
			t.Errorf("%s: %+v", c.item, res)
		}
	}
}

func TestResolveRefuses(t *testing.T) {
	r := rig()
	ctx := context.Background()
	for item, want := range map[string]error{
		"":             ErrBadItem,
		"vlc":          ErrBadItem,
		"app:":         ErrBadItem,
		"shell:rm":     ErrBadItem,
		"app:../x":     ErrBadItem,
		"tool:-rf":     ErrBadItem,
		"package:-x":   ErrBadItem,
		"package:a/b":  ErrBadItem,
		"package:..":   ErrBadItem,
		"app:nope":     ErrNotFound,
		"package:gone": ErrNotFound, // config files only: nothing installed to remove
		"package:nope": ErrNotFound,
		"snap:nope":    ErrNotFound,
		"app:lost":     ErrNoOwner,
		"tool:odd":     ErrNoOwner,
		"game:nope":    ErrNotFound,
	} {
		if _, err := r.Resolve(ctx, item); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", item, err, want)
		}
	}
}

func TestProgram(t *testing.T) {
	// program() is for matching only: a quoted path with a space keeps
	// its first word, and a wrong guess matches nothing.
	for exec, want := range map[string]string{
		"/usr/bin/vlc --x %U": "vlc", `"/opt/My App/run" %f`: "My", "env A=1 B=2 code %F": "code", "": "", "env": "env",
	} {
		if got := program(exec); got != want {
			t.Errorf("%q: %q", exec, got)
		}
	}
}
