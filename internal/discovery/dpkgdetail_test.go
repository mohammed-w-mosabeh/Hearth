package discovery

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDepNames(t *testing.T) {
	got := DepNames("libc6 (>= 2.34), libfoo1 | libbar1:any, python3:any (>= 3.11~), libc6, ")
	if want := []string{"libc6", "libfoo1", "libbar1", "python3"}; !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
	if len(DepNames("")) != 0 {
		t.Fatal("empty")
	}
}

func TestDpkgDetail(t *testing.T) {
	dir := t.TempDir()
	status := filepath.Join(dir, "status")
	write(t, status, `Package: vim
Status: install ok installed
Architecture: amd64
Version: 2:9.1
Depends: vim-common (= 2:9.1), libc6 (>= 2.34)

Package: vim-common
Status: install ok installed
Architecture: all
Version: 2:9.1

Package: libc6
Status: install ok installed
Architecture: amd64
Multi-Arch: same
Version: 2.41

Package: vim-tiny
Status: deinstall ok config-files
Architecture: amd64
Version: 2:9.0
Depends: vim-common

Package: editor-tools
Status: install ok installed
Architecture: all
Version: 1
Depends: vim | nano, vim-common
`)
	// A real file and a directory: directories are left out.
	real := filepath.Join(dir, "usr-bin-vim")
	write(t, real, "")
	info := filepath.Join(dir, "info")
	if err := os.Mkdir(info, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(info, "vim-common.list"), "/.\n"+dir+"\n"+real+"\n/nonexistent/doc\n")
	write(t, filepath.Join(info, "libc6:amd64.list"), "/lib/libc.so.6\n")
	d := NewDpkg(DpkgConfig{Status: status, ExtendedState: filepath.Join(dir, "none")})

	det, ok, err := d.Detail("vim-common:all")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !slices.Equal(det.ReverseDeps, []string{"editor-tools", "vim"}) { // not the config-files vim-tiny
		t.Errorf("rdeps %v", det.ReverseDeps)
	}
	if !slices.Equal(det.Files, []string{real, "/nonexistent/doc"}) {
		t.Errorf("files %v", det.Files)
	}
	if det, _, _ := d.Detail("vim:amd64"); !slices.Equal(det.Deps, []string{"vim-common", "libc6"}) || len(det.Files) != 0 {
		t.Errorf("vim %+v", det)
	}
	if det, _, _ := d.Detail("libc6:amd64"); !slices.Equal(det.Files, []string{"/lib/libc.so.6"}) {
		t.Errorf("multi-arch list %v", det.Files)
	}
	if _, ok, _ := d.Detail("vim:i386"); ok {
		t.Error("wrong arch found")
	}

	for path, code := range map[string]int{"vim:amd64": 200, "nope:amd64": 404, "vim": 400} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/pkgs/apt/x", nil)
		r.SetPathValue("id", path)
		d.HandleDetail(rec, r)
		if rec.Code != code {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestDpkgDetailFileCap(t *testing.T) {
	dir := t.TempDir()
	status := filepath.Join(dir, "status")
	write(t, status, "Package: big\nStatus: install ok installed\nArchitecture: all\nVersion: 1\n")
	if err := os.Mkdir(filepath.Join(dir, "info"), 0o750); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < maxFiles+10; i++ {
		fmt.Fprintf(&b, "/nonexistent/%d\n", i)
	}
	write(t, filepath.Join(dir, "info", "big.list"), b.String())
	det, _, _ := NewDpkg(DpkgConfig{Status: status}).Detail("big:all")
	if len(det.Files) != maxFiles || !det.FilesTruncated {
		t.Fatalf("%d %v", len(det.Files), det.FilesTruncated)
	}
}
