package rules

import (
	"slices"
	"strings"
	"testing"
)

func TestTarShapes(t *testing.T) {
	a := ArchivePath(op, 3)
	m := MovedMember(op, 3)
	if err := CheckTarCreate(TarCreateArgs(a, "/etc", m)); err != nil {
		t.Fatal(err)
	}
	if err := CheckTarCreate(TarCreateArgs(a, "/", "etc/vlc/vlcrc")); err != nil {
		t.Fatal(err)
	}
	if err := CheckTarList(TarListArgs(a)); err != nil {
		t.Fatal(err)
	}
	if err := CheckTarExtract(TarExtractArgs(a, "/opt", m)); err != nil {
		t.Fatal(err)
	}
	if err := CheckTarCat(TarCatArgs(a, "etc/vlc/vlcrc")); err != nil {
		t.Fatal(err)
	}
	bad := [][]string{
		TarCreateArgs("/tmp/x.tar.zst", "/etc", m), TarCreateArgs(a, "/", m), TarCreateArgs(a, "/etc", "etc/vlc"),
		TarCreateArgs(a, "/etc", "../x"), TarCreateArgs(a, "/home", m), TarCreateArgs(a, "/etc", "-x"),
		TarCreateArgs(a, "/etc", "etc/passwd"), TarCreateArgs(a, "/", "etc/hearth/x"),
		TarCreateArgs("/var/lib/hearth/removed/"+op+"/../x.tar.zst", "/etc", m),
		append(TarCreateArgs(a, "/etc", m), "--to-command=sh"),
		TarExtractArgs(a, "/", "etc/vlc/vlcrc"), TarExtractArgs(a, "/home/u", m), TarCatArgs(a, m),
		TarCatArgs(a, "etc/../shadow"), TarListArgs("/etc/shadow"),
	}
	for _, args := range bad {
		var err error
		switch {
		case slices.Contains(args, "--create"):
			err = CheckTarCreate(args)
		case slices.Contains(args, "--to-stdout"):
			err = CheckTarCat(args)
		case slices.Contains(args, "--extract"):
			err = CheckTarExtract(args)
		default:
			err = CheckTarList(args)
		}
		if err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

// Only the fixed shapes pass: no option tar would run a program for, no
// folder outside the roots.
func FuzzCheckTarCreate(f *testing.F) {
	f.Add(ArchivePath(op, 1), "/etc", MovedMember(op, 1))
	f.Add(ArchivePath(op, 1), "/", "etc/a/b")
	f.Fuzz(func(t *testing.T, a, d, m string) {
		if CheckTarCreate(TarCreateArgs(a, d, m)) != nil {
			return
		}
		if !strings.HasPrefix(a, RemovedDir+"/") || strings.Contains(a, "..") || strings.HasPrefix(m, "-") ||
			strings.Contains(m, "..") || (d != "/" && !slices.Contains(SystemRoots, d)) {
			t.Fatalf("accepted %q %q %q", a, d, m)
		}
	})
}
