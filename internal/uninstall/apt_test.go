package uninstall

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/pkgs"
)

const simOut = `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
Reading package lists...
Building dependency tree...
Reading state information...
The following packages will be REMOVED:
  libold1 libvlc5 vlc vlc-data
0 upgraded, 0 newly installed, 4 to remove and 0 not upgraded.
Remv libold1 [1.0-1]
Remv vlc [3.0.21-1]
Remv vlc-data [3.0.21-1]
Remv libvlc5:i386 [3.0.21-1] [3.0.22-1]
Remv half
`

func TestParseSim(t *testing.T) {
	rm, err := ParseSim(simOut)
	if err != nil {
		t.Fatal(err)
	}
	want := []SimRemoval{{"libold1", "", "1.0-1"}, {"vlc", "", "3.0.21-1"}, {"vlc-data", "", "3.0.21-1"},
		{"libvlc5", "i386", "3.0.21-1"}, {"half", "", ""}}
	if !slices.Equal(rm, want) {
		t.Fatalf("%+v", rm)
	}
	for _, bad := range []string{
		"Inst libx [1]\n", "Conf libx [1]\n", "Purg libx\n", "E: Unable to locate package nope\n",
		"Remv -x [1]\n", "Remv ../x [1]\n", "Remv a/b [1]\n", "Remv X [1]\n", "Remv vlc [1] extra\n",
		strings.Repeat("\n", maxSimLines+1),
	} {
		if _, err := ParseSim(bad); err == nil {
			t.Errorf("accepted %q", bad[:min(len(bad), 40)])
		}
	}
}

func installed() []discovery.Package {
	return []discovery.Package{
		{Name: "vlc", Arch: "amd64", State: "installed"},
		{Name: "vlc-data", Arch: "all", State: "installed", Auto: true},
		{Name: "libvlc5", Arch: "amd64", State: "installed", Auto: true},
		{Name: "libvlc5", Arch: "i386", State: "installed", Auto: true},
		{Name: "libold1", Arch: "amd64", State: "installed", Auto: true},      // already autoremovable
		{Name: "libmine", Arch: "amd64", State: "installed"},                  // installed by hand
		{Name: "vlc-plugin-x", Arch: "amd64", State: "installed", Auto: true}, // depends on vlc: taken along
		{Name: "gone", Arch: "amd64", State: "config-files", Auto: true},
	}
}

func TestOrphans(t *testing.T) {
	before := []SimRemoval{{Name: "libold1"}}
	after := []SimRemoval{{Name: "libold1"}, {Name: "vlc"}, {Name: "vlc-data"}, {Name: "libvlc5"},
		{Name: "libvlc5", Arch: "i386"}, {Name: "libmine"}, {Name: "vlc-plugin-x"}, {Name: "gone"}, {Name: "unknown"}}
	orphans, kept := Orphans(before, after, []string{"vlc:amd64"}, []string{"vlc-plugin-x:amd64"}, installed(), "amd64")
	if want := []string{"libvlc5:amd64", "libvlc5:i386", "vlc-data:all"}; !slices.Equal(orphans, want) {
		t.Fatalf("orphans %v", orphans)
	}
	if len(kept) != 1 || kept[0] != (Kept{"libmine:amd64", "manual"}) {
		t.Fatalf("kept %+v", kept)
	}
	// Nothing new: nothing offered.
	if o, _ := Orphans(after, after, nil, nil, installed(), "amd64"); len(o) != 0 {
		t.Fatalf("%v", o)
	}
}

func TestGuard(t *testing.T) {
	g := Guard{Running: "6.12.48+deb13-amd64", Installed: []discovery.Package{
		{Name: "bash", Arch: "amd64", State: "installed", Essential: true},
		{Name: "apt", Arch: "amd64", State: "installed", Priority: "required"},
		{Name: "sudo", Arch: "amd64", State: "installed", Protected: true},
		{Name: "vlc", Arch: "amd64", State: "installed"},
		{Name: "linux-image-6.12.48+deb13-amd64", Arch: "amd64", State: "installed"},
		{Name: "linux-image-6.12.43+deb13-amd64", Arch: "amd64", State: "installed"},
		{Name: "linux-image-6.12.38+deb13-amd64", Arch: "amd64", State: "installed"},
		{Name: "linux-image-6.12.30+deb13-amd64", Arch: "amd64", State: "installed"},
		{Name: "linux-image-amd64", Arch: "amd64", State: "installed"},
	}}
	for id, want := range map[string]string{
		"bash": BlockEssential, "bash:amd64": BlockEssential, "apt:amd64": BlockRequired, "sudo": BlockProtected,
		"hearth": BlockSelf, "hearth:amd64": BlockSelf, "vlc": "", "vlc:amd64": "",
		"linux-image-6.12.48+deb13-amd64": BlockKernel, // running
		"linux-image-6.12.43+deb13-amd64": BlockKernel, // second newest
		"linux-image-6.12.38+deb13-amd64": "",          // old: may go
		"linux-image-6.12.30+deb13-amd64": "",
		"linux-image-amd64":               BlockKernel, // the meta package
	} {
		if got := g.Blocked(id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	g.Running = ""
	if g.Blocked("linux-image-6.12.30+deb13-amd64") != BlockKernel {
		t.Error("without the running release every kernel must be kept")
	}
}

func TestDesktop(t *testing.T) {
	g := Guard{Installed: []discovery.Package{
		{Name: "mydm", Arch: "amd64", Provides: "x-display-manager"},
		{Name: "mywm", Arch: "amd64", Provides: "foo, x-window-manager (= 1)"},
		{Name: "vlc", Arch: "amd64", Provides: "mp3-decoder"},
	}}
	for id, want := range map[string]bool{
		"gnome-shell": true, "gdm3:amd64": true, "gnome-session-bin": true, "kwin-wayland": true, "xserver-xorg-core": true,
		"mydm": true, "mywm:amd64": true, "vlc": false, "gnome-shell-extension-x": false, "custom": false,
	} {
		if got := g.Desktop(id, nil); got != want {
			t.Errorf("%s: %v", id, got)
		}
	}
	if !g.Desktop("custom", map[string]bool{"custom": true}) {
		t.Error("the session's own package is a desktop package")
	}
}

func TestCheckTransaction(t *testing.T) {
	plan := []string{"vlc:amd64", "vlc-data:all"}
	if err := CheckTransaction([]string{"vlc:amd64"}, plan); err != nil {
		t.Fatal(err)
	}
	if err := CheckTransaction([]string{"vlc:amd64", "libc6:amd64"}, plan); err == nil || !strings.Contains(err.Error(), "libc6") {
		t.Fatal(err)
	}
}

func TestSessionPrograms(t *testing.T) {
	proc := t.TempDir()
	mk := func(pid, comm, exe string) {
		d := filepath.Join(proc, pid)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(exe, filepath.Join(d, "exe")); err != nil {
			t.Fatal(err)
		}
	}
	mk("10", "gnome-shell", "/usr/bin/gnome-shell")
	mk("11", "bash", "/usr/bin/bash")
	mk("12", "gnome-shell", "/usr/bin/gnome-shell")
	mk("self", "sway", "/usr/bin/sway")
	etc := t.TempDir()
	dm := filepath.Join(etc, "display-manager.service")
	if err := os.Symlink("/usr/lib/systemd/system/gdm3.service", dm); err != nil {
		t.Fatal(err)
	}
	got := SessionPrograms(proc, os.Getuid(), dm)
	want := []string{"/usr/lib/systemd/system/gdm3.service", "/usr/bin/gnome-shell"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
	if got := SessionPrograms(proc, os.Getuid()+1, filepath.Join(etc, "none")); len(got) != 0 {
		t.Fatalf("another user's processes: %v", got)
	}
}

// Whatever apt prints, an accepted removal is a valid package name with an
// optional architecture, and nothing that installs is accepted.
func FuzzParseSim(f *testing.F) {
	f.Add(simOut)
	f.Add("Remv a:b [1] [2]\nInst x\n")
	f.Fuzz(func(t *testing.T, out string) {
		rm, err := ParseSim(out)
		if err != nil {
			return
		}
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "Inst ") || strings.HasPrefix(l, "Conf ") || strings.HasPrefix(l, "E: ") {
				t.Fatalf("accepted %q", l)
			}
		}
		for _, r := range rm {
			id := r.Name
			if r.Arch != "" {
				id += ":" + r.Arch
			}
			if _, _, err := pkgs.Target(id); err != nil || strings.ContainsAny(r.Version, " ]\n") {
				t.Fatalf("accepted %+v", r)
			}
		}
	})
}
