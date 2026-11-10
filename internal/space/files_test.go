package space

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

func (f *fixture) files(t *testing.T, target string, params map[string]string) (actions.Action, actions.Request, actions.Plan) {
	t.Helper()
	a, ok := f.reg.Lookup("files/trash")
	if !ok {
		t.Fatal("files/trash")
	}
	r := actions.Request{Target: target, Params: params}
	if err := a.Validate(r); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return a, r, p
}

func old(t *testing.T, path string, days int) {
	t.Helper()
	when := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestFilesValidate(t *testing.T) {
	f := setup(t)
	a, _ := f.reg.Lookup("files/trash")
	if a.Risk != actions.Destructive || f.reg.AuthorityOf("files/trash", actions.Request{}) != actions.AuthorityNone {
		t.Fatal("files/trash is destructive and never asks for a password")
	}
	bad := []actions.Request{
		{Target: "Downloads", Params: map[string]string{"pattern": "*"}},
		{Target: "/tmp", Params: map[string]string{"pattern": "*"}},
		{Target: "~/../etc", Params: map[string]string{"pattern": "*"}},
		{Target: "~/a/../../etc", Params: map[string]string{"pattern": "*"}},
		{Target: "~/.ssh", Params: map[string]string{"pattern": "*"}},
		{Target: "~/a/.git", Params: map[string]string{"pattern": "*"}},
		{Target: "~/Downloads", Params: map[string]string{}},
		{Target: "~/Downloads", Params: map[string]string{"pattern": "a/b"}},
		{Target: "~/Downloads", Params: map[string]string{"pattern": "[a-z]*"}},
		{Target: "~/Downloads", Params: map[string]string{"pattern": `\*`}},
		{Target: "~/Downloads", Params: map[string]string{"pattern": "*", "days": "-1"}},
		{Target: "~/Downloads", Params: map[string]string{"pattern": "*", "days": "07"}},
		{Target: "~/Downloads", Params: map[string]string{"pattern": "*", "days": "99999"}},
		{Target: "~/Downloads", Params: map[string]string{"pattern": "*", "rm": "-rf"}},
	}
	for _, r := range bad {
		if a.Validate(r) == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	if err := a.Validate(actions.Request{Target: "~/Downloads/old isos", Params: map[string]string{"pattern": "*.iso", "days": "30"}}); err != nil {
		t.Fatal(err)
	}
}

func TestFilesPlanAndRun(t *testing.T) {
	f := setup(t)
	dir := filepath.Join(f.home, "Downloads")
	for _, n := range []string{"a.iso", "b.iso", "new.iso", "c.txt", ".hidden.iso", "sub/d.iso"} {
		write(t, filepath.Join(dir, n), 10)
	}
	for _, n := range []string{"a.iso", "b.iso", "c.txt", ".hidden.iso", "sub/d.iso"} {
		old(t, filepath.Join(dir, n), 40)
	}
	if err := os.Symlink(filepath.Join(dir, "a.iso"), filepath.Join(dir, "link.iso")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder.iso"), 0o700); err != nil {
		t.Fatal(err)
	}
	a, r, p := f.files(t, "~/Downloads", map[string]string{"pattern": "*.iso", "days": "30"})
	var names []string
	for _, it := range p.Items {
		names = append(names, it.Name)
	}
	if !slices.Equal(names, []string{"a.iso", "b.iso"}) || len(p.Blocked) != 0 {
		t.Fatalf("only old, matching, direct, regular, non-hidden files: %v %+v", names, p.Blocked)
	}
	res, err := a.Run(context.Background(), r, p)
	if err != nil || res.Done != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	want := []string{"gio", "trash", "--", filepath.Join(dir, "a.iso"), filepath.Join(dir, "b.iso")}
	if len(f.runs) != 1 || !slices.Equal(f.runs[0], want) {
		t.Fatalf("%v", f.runs)
	}
}

func TestFilesAtMost16OldestFirst(t *testing.T) {
	f := setup(t)
	dir := filepath.Join(f.home, "Downloads")
	for i := range 20 {
		p := filepath.Join(dir, fmt.Sprintf("f%02d.log", i))
		write(t, p, 1)
		old(t, p, 100-i)
	}
	_, _, p := f.files(t, "~/Downloads", map[string]string{"pattern": "*.log"})
	if len(p.Items) != maxSelected || p.Items[0].Name != "f00.log" || len(p.Blocked) != 4 {
		t.Fatalf("%d items, first %s, %d blocked", len(p.Items), p.Items[0].Name, len(p.Blocked))
	}
}

func TestFilesRefusesLinkedFolderAndSwaps(t *testing.T) {
	f := setup(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "x.iso"), 1)
	if err := os.Symlink(outside, filepath.Join(f.home, "Linked")); err != nil {
		t.Fatal(err)
	}
	_, _, p := f.files(t, "~/Linked", map[string]string{"pattern": "*"})
	if len(p.Items) != 0 || len(p.Blocked) != 1 || !strings.Contains(p.Blocked[0].Detail, "link") {
		t.Fatalf("%+v", p)
	}
	_, _, p = f.files(t, "~/Missing", map[string]string{"pattern": "*"})
	if len(p.Items) != 0 || len(p.Blocked) != 1 {
		t.Fatalf("%+v", p)
	}
	// A file swapped after the preview is not trashed.
	file := filepath.Join(f.home, "Downloads/a.iso")
	write(t, file, 1)
	a, r, p := f.files(t, "~/Downloads", map[string]string{"pattern": "*.iso"})
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.home, "busy"), 1)
	write(t, file, 2)
	res, err := a.Run(context.Background(), r, p)
	if err != nil || res.Done != 0 || len(res.Failed) != 1 || len(f.runs) != 0 {
		t.Fatalf("%v %+v %v", err, res, f.runs)
	}
}

func TestSpacePreview(t *testing.T) {
	f := setup(t)
	write(t, filepath.Join(f.home, ".cache/big/x"), 1<<20)
	a, r, p := f.plan(t, "preview", "space", map[string]string{})
	if a.Risk != actions.Safe || f.reg.AuthorityOf("space/preview", r) != actions.AuthorityNone {
		t.Fatal("space/preview is safe and never asks")
	}
	if len(p.Items) == 0 || p.Items[0].ID != "cache" {
		t.Fatalf("%+v", p)
	}
	res, err := a.Run(context.Background(), r, p)
	if err != nil || res.Done != len(p.Items) || len(f.runs) != 0 || len(f.calls) != 0 {
		t.Fatalf("a preview changes nothing: %v %+v %v %v", err, res, f.runs, f.calls)
	}
	if a.Validate(actions.Request{Target: "trash", Params: map[string]string{}}) == nil {
		t.Fatal("target must be space")
	}
}
