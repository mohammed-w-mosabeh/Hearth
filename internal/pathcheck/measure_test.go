package pathcheck

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckMeasure(t *testing.T) {
	res := fake(map[string]string{"/srv": "/srv", "/srv/site": "/srv/site", "/mnt/disk": "/mnt/disk",
		"/mnt/disk/media": "/mnt/disk/media", BackupsParent: BackupsParent})
	measured := []string{}
	res.Measure = func(real string) Size {
		measured = append(measured, real)
		return Size{Bytes: int64(len(real)), Partial: real == "/mnt/disk/media"}
	}
	r := res.CheckMeasure(specJSON(t, []string{"/srv", "/mnt/disk"}, "/srv/site", "/mnt/disk/media", "/srv/site"))
	if !r.OK {
		t.Fatal(r.Refused)
	}
	if r.BackupDev != "8:1" {
		t.Fatalf("backup dev %q", r.BackupDev)
	}
	if len(measured) != 2 || r.Sizes["/srv/site"].Bytes != 9 || !r.Sizes["/mnt/disk/media"].Partial {
		t.Fatalf("sizes %v, measured %v", r.Sizes, measured)
	}
	if r.Binds[1].Dev == r.BackupDev {
		t.Fatal("the disk's bind is on the backups' filesystem")
	}
	// A refused spec is not measured.
	measured = nil
	if r := res.CheckMeasure(specJSON(t, []string{"/srv"}, "/srv/../etc")); r.OK || len(measured) != 0 {
		t.Fatal("a refused spec measured")
	}
	// Without the backups' folder there is no answer.
	res2 := fake(map[string]string{"/srv": "/srv", "/srv/site": "/srv/site"})
	if r := res2.CheckMeasure(specJSON(t, []string{"/srv"}, "/srv/site")); r.OK {
		t.Fatal("measured without the backups' device")
	}
}

func TestServeMeasure(t *testing.T) {
	res := fake(map[string]string{"/srv": "/srv", "/srv/site": "/srv/site", BackupsParent: BackupsParent})
	res.Measure = func(string) Size { return Size{Bytes: 5} }
	req, _ := json.Marshal(Request{Spec: specJSON(t, []string{"/srv"}, "/srv/site"), Measure: true})
	var out bytes.Buffer
	if err := Serve(res, bytes.NewReader(append(req, '\n')), &out); err != nil {
		t.Fatal(err)
	}
	var r Reply
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || !r.OK || r.Sizes["/srv/site"].Bytes != 5 || r.BackupDev == "" {
		t.Fatalf("%s", out.String())
	}
	// Without measure the sizes stay out.
	req, _ = json.Marshal(Request{Spec: specJSON(t, []string{"/srv"}, "/srv/site")})
	out.Reset()
	if err := Serve(res, bytes.NewReader(append(req, '\n')), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sizes") {
		t.Fatalf("%s", out.String())
	}
}

func TestWalk(t *testing.T) {
	d := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "big"), make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	for p, n := range map[string]int{"f": 10, "a/g": 20, "a/b/h": 30} {
		if err := os.WriteFile(filepath.Join(d, p), make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "big"), filepath.Join(d, "flink")); err != nil {
		t.Fatal(err)
	}
	if s := Walk(d, time.Minute, 1000); s.Bytes != 60 || s.Partial {
		t.Fatalf("walk %+v", s)
	}
	if s := Walk(d, time.Minute, 2); !s.Partial {
		t.Fatalf("no bound: %+v", s)
	}
	if s := Walk(filepath.Join(d, "f"), time.Minute, 10); s.Bytes != 10 {
		t.Fatalf("file %+v", s)
	}
	if s := Walk(filepath.Join(d, "missing"), time.Minute, 10); !s.Partial {
		t.Fatalf("missing %+v", s)
	}
	if s := Walk(filepath.Join(d, "link"), time.Minute, 10); s.Bytes != 0 {
		t.Fatalf("followed a link: %+v", s)
	}
}
