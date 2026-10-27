package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const statusFixture = `Package: bash
Essential: yes
Status: install ok installed
Priority: required
Section: shells
Installed-Size: 7196
Maintainer: Matthias Klose <doko@debian.org>
Architecture: amd64
Multi-Arch: foreign
Version: 5.2.37-2+b10
Replaces: bash-completion (<< 20060301-0)
Depends: base-files (>= 2.1.12),
 debianutils (>= 5.6-0.1)
Pre-Depends: libc6 (>= 2.38)
Description: GNU Bourne Again SHell
 Bash is an sh-compatible command language interpreter.
Conffiles:
 /etc/bash.bashrc 89269e1298235f1b12b4c16e4065ad0d

Package: libfoo1
Status: hold ok installed
Priority: optional
Architecture: i386
Source: foo (1.2-3)
Version: 1.2-3
Description: foo library

Package: old-tool
Status: deinstall ok config-files
Architecture: amd64
Version: 0.9

Package: purged
Status: purge ok not-installed
Architecture: amd64

Package: systemd
Status: install ok installed
Protected: yes
Priority: important
Architecture: amd64
Version: 257.9-1
`

const extendedFixture = `Package: libfoo1
Architecture: i386
Auto-Installed: 1

Package: bash
Architecture: amd64
Auto-Installed: 0
`

func TestParseStatus(t *testing.T) {
	pkgs, err := ParseStatus(strings.NewReader(statusFixture))
	if err != nil || len(pkgs) != 4 {
		t.Fatalf("%v %+v", err, pkgs)
	}
	b := pkgs[0]
	if b.Name != "bash" || b.Arch != "amd64" || b.State != "installed" || !b.Essential || b.Priority != "required" ||
		b.InstalledKiB != 7196 || b.Summary != "GNU Bourne Again SHell" || b.Hold ||
		b.Depends != "base-files (>= 2.1.12), debianutils (>= 5.6-0.1), libc6 (>= 2.38)" {
		t.Errorf("bash: %+v", b)
	}
	if f := pkgs[1]; !f.Hold || f.Source != "foo" || f.Undeletable() != "" {
		t.Errorf("libfoo1: %+v", f)
	}
	if o := pkgs[2]; o.State != "config-files" {
		t.Errorf("old-tool: %+v", o)
	}
	for p, want := range map[string]string{"bash": "essential", "systemd": "protected", "libfoo1": ""} {
		for _, x := range pkgs {
			if x.Name == p && x.Undeletable() != want {
				t.Errorf("%s undeletable %q, want %q", p, x.Undeletable(), want)
			}
		}
	}
	if (Package{Name: "hearth"}).Undeletable() != "hearth" || (Package{Name: "x", Priority: "required"}).Undeletable() != "required" {
		t.Error("hearth or required not protected")
	}
}

func TestParseStatusBoundsFields(t *testing.T) {
	long := "Package: x\nStatus: install ok installed\nArchitecture: all\nDescription: " + strings.Repeat("d", 10000) + "\nDepends: a"
	for range 3000 {
		long += ",\n a"
	}
	pkgs, err := ParseStatus(strings.NewReader(long + "\n"))
	if err != nil || len(pkgs) != 1 || len(pkgs[0].Summary) != maxField || len(pkgs[0].Depends) > maxField+16 {
		t.Fatalf("%v %d %d", err, len(pkgs[0].Summary), len(pkgs[0].Depends))
	}
}

func TestDpkgReadsAndCaches(t *testing.T) {
	dir := t.TempDir()
	status, ext := filepath.Join(dir, "status"), filepath.Join(dir, "extended_states")
	_ = os.WriteFile(status, []byte(statusFixture), 0o600)
	_ = os.WriteFile(ext, []byte(extendedFixture), 0o600)
	d := NewDpkg(DpkgConfig{Status: status, ExtendedState: ext})
	pkgs, err := d.Packages()
	if err != nil || len(pkgs) != 4 || pkgs[0].Name != "bash" || pkgs[1].Name != "libfoo1" || !pkgs[1].Auto || pkgs[0].Auto {
		t.Fatalf("%v %+v", err, pkgs)
	}
	if again, _ := d.Packages(); &again[0] != &pkgs[0] {
		t.Error("not cached")
	}
	if got, _ := d.ByName("systemd"); len(got) != 1 || !got[0].Protected {
		t.Errorf("ByName %+v", got)
	}
	// dpkg rewrites the file: the next read sees it.
	_ = os.WriteFile(status, []byte(statusFixture+"\nPackage: zsh\nStatus: install ok installed\nArchitecture: amd64\n"), 0o600)
	later := time.Now().Add(time.Second)
	_ = os.Chtimes(status, later, later)
	if pkgs, _ := d.Packages(); len(pkgs) != 5 || pkgs[4].Name != "zsh" {
		t.Fatalf("not reread: %+v", pkgs)
	}
}

// pkgsHub plays api.Hub for PkgsTopic.
type pkgsHub struct {
	mu    sync.Mutex
	subs  int
	msgs  int
	watch chan struct{}
}

func (h *pkgsHub) Publish(string, any) error    { h.mu.Lock(); h.msgs++; h.mu.Unlock(); return nil }
func (h *pkgsHub) Subscribers(string) int       { h.mu.Lock(); defer h.mu.Unlock(); return h.subs }
func (h *pkgsHub) Watch(string) <-chan struct{} { return h.watch }
func (h *pkgsHub) published() int               { h.mu.Lock(); defer h.mu.Unlock(); return h.msgs }

func TestDpkgWatchesOnlyWhileFollowed(t *testing.T) {
	dir := t.TempDir()
	status := filepath.Join(dir, "status")
	_ = os.WriteFile(status, []byte(statusFixture), 0o600)
	h := &pkgsHub{watch: make(chan struct{}, 1)}
	d := NewDpkg(DpkgConfig{Status: status, ExtendedState: filepath.Join(dir, "extended_states"), Pub: h})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	replace := func() { // as dpkg does: write a new file, rename it over
		tmp := status + "-new"
		_ = os.WriteFile(tmp, []byte(statusFixture), 0o600)
		_ = os.Rename(tmp, status)
	}
	replace() // nobody follows: not watched
	time.Sleep(700 * time.Millisecond)
	if h.published() != 0 {
		t.Fatal("published without subscribers")
	}
	h.mu.Lock()
	h.subs = 1
	h.mu.Unlock()
	h.watch <- struct{}{}
	time.Sleep(100 * time.Millisecond) // the watcher starts
	for range 5 {                      // a burst is one message
		replace()
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.published() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(700 * time.Millisecond)
	if n := h.published(); n != 1 {
		t.Fatalf("published %d times", n)
	}
}
