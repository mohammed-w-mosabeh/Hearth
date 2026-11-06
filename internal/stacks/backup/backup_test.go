package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func goodPlan() Plan {
	return Plan{Stack: "web", Output: PartialName("20261106T101500Z"),
		Paths: []string{StackFolder("web"), VolumeData("web_db"), "/home/u/site"}}
}

func TestStampAndNames(t *testing.T) {
	s := Stamp(time.Date(2026, 11, 6, 10, 15, 0, 0, time.FixedZone("x", 3600)))
	if s != "20261106T091500Z" {
		t.Fatalf("stamp %s", s)
	}
	if !ValidArchive(ArchiveName(s)) || ManifestOf(ArchiveName(s)) != s+".json" {
		t.Fatal("names")
	}
	for _, bad := range []string{"", "x.tar.zst", "../20261106T091500Z.tar.zst", "20261106T091500Z.tar.zst/", "20261106T091500Z.tar.gz", ".20261106T091500Z.partial"} {
		if ValidArchive(bad) {
			t.Errorf("%q valid", bad)
		}
	}
}

func TestPlanCheck(t *testing.T) {
	if err := goodPlan().Check(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Plan){
		"stack name":        func(p *Plan) { p.Stack = "../x" },
		"output":            func(p *Plan) { p.Output = "../../etc/x" },
		"output archive":    func(p *Plan) { p.Output = "20261106T101500Z.tar.zst" },
		"no paths":          func(p *Plan) { p.Paths = nil },
		"relative":          func(p *Plan) { p.Paths = append(p.Paths, "home/u") },
		"unclean":           func(p *Plan) { p.Paths = append(p.Paths, "/home/u/../../etc") },
		"root":              func(p *Plan) { p.Paths = append(p.Paths, "/") },
		"option":            func(p *Plan) { p.Paths = append(p.Paths, "/home/-rf") },
		"newline":           func(p *Plan) { p.Paths = append(p.Paths, "/home/u\nx") },
		"twice":             func(p *Plan) { p.Paths = append(p.Paths, "/home/u/site") },
		"backups":           func(p *Plan) { p.Paths = append(p.Paths, Dir) },
		"in backups":        func(p *Plan) { p.Paths = append(p.Paths, Dir+"/web") },
		"holds backups":     func(p *Plan) { p.Paths = append(p.Paths, "/var/lib") },
		"other stack":       func(p *Plan) { p.Paths = append(p.Paths, StackFolder("db")) },
		"volume not _data":  func(p *Plan) { p.Paths = append(p.Paths, VolumesDir+"/x") },
		"volume sub":        func(p *Plan) { p.Paths = append(p.Paths, VolumesDir+"/x/_data/y") },
		"volumes":           func(p *Plan) { p.Paths = append(p.Paths, VolumesDir) },
		"no own folder":     func(p *Plan) { p.Paths = p.Paths[1:] },
		"too many":          func(p *Plan) { p.Paths = append(p.Paths, manyPaths(MaxPaths)...) },
		"stack folder file": func(p *Plan) { p.Paths = append(p.Paths, StackFolder("web")+"/secrets.env") },
	}
	for name, f := range bad {
		p := goodPlan()
		f(&p)
		if err := p.Check(); err == nil {
			t.Errorf("%s: accepted %v", name, p.Paths)
		}
	}
}

func manyPaths(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "/home/u/d" + strings.Repeat("x", i+1)
	}
	return out
}

func TestTarArgs(t *testing.T) {
	p := goodPlan()
	args := TarCreate(p)
	if err := CheckTarCreate(args); err != nil {
		t.Fatal(err)
	}
	want := []string{"--create", "--zstd", "--file=/var/lib/hearth/backups/web/.20261106T101500Z.partial", "--directory=/",
		"--numeric-owner", "--sparse", "--one-file-system", "--warning=no-file-ignored", "--",
		"var/lib/hearth/stacks/web", "var/lib/docker/volumes/web_db/_data", "home/u/site"}
	if !slices.Equal(args, want) {
		t.Fatalf("args %q", args)
	}
	for i := range args {
		for _, swap := range []string{"--dereference", "-h", "--to-command=x", "/etc", "--file=/etc/x", ""} {
			b := slices.Clone(args)
			b[i] = swap
			if CheckTarCreate(b) == nil {
				t.Errorf("accepted %q at %d", swap, i)
			}
		}
	}
	if CheckTarCreate(append(slices.Clone(args), "etc")) != nil {
		t.Error("a further bind refused") // one more path is a valid plan
	}
	if CheckTarCreate(append(slices.Clone(args), "--checkpoint-action=exec=sh")) == nil {
		t.Error("an option after -- accepted")
	}
	r := RestorePlan{Stack: "web", Archive: "20261106T101500Z.tar.zst", SHA256: strings.Repeat("a", 64), Volumes: []string{"web_db", "x"}, Secrets: true}
	ex := TarExtract(r)
	if err := CheckTarExtract(ex); err != nil {
		t.Fatal(err)
	}
	if ex[len(ex)-1] != "var/lib/hearth/stacks/web/secrets.env" || ex[len(ex)-3] != "var/lib/docker/volumes/web_db/_data" {
		t.Fatalf("extract %q", ex)
	}
	for _, extra := range []string{"etc/shadow", "var/lib/hearth/stacks/web/spec.json", "var/lib/hearth/stacks/db/secrets.env",
		"var/lib/docker/volumes/x/_data/../../y/_data", "home/u/site"} {
		if CheckTarExtract(append(slices.Clone(ex), extra)) == nil {
			t.Errorf("extract of %s accepted", extra)
		}
	}
	b := slices.Clone(ex)
	b[2] = "--file=/var/lib/hearth/backups/web/../db/20261106T101500Z.tar.zst"
	if CheckTarExtract(b) == nil {
		t.Error("another stack's archive accepted")
	}
}

func TestPrune(t *testing.T) {
	names := []string{"20261103T000000Z.tar.zst", "20261101T000000Z.tar.zst", "x", "20261102T000000Z.json", "20261102T000000Z.tar.zst"}
	if got := Prune(names, 2); !slices.Equal(got, []string{"20261101T000000Z.tar.zst"}) {
		t.Fatalf("prune %v", got)
	}
	if got := Prune(names, 0); len(got) != 2 {
		t.Fatalf("keep 0 is keep 1: %v", got)
	}
	if Prune(names, 7) != nil {
		t.Fatal("pruned under keep")
	}
}

func TestParseManifest(t *testing.T) {
	m := Manifest{Version: 1, Stack: "web", Archive: "20261106T101500Z.tar.zst", SHA256: strings.Repeat("a", 64), Size: 10,
		Spec: json.RawMessage(`{}`), Volumes: []Volume{{Name: "web_db"}}}
	b, _ := json.Marshal(m)
	if _, err := ParseManifest(b, "web", m.Archive); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"other archive": strings.Replace(string(b), "20261106T101500Z", "20261106T101501Z", 1),
		"unknown key":   strings.Replace(string(b), `{"version"`, `{"x":1,"version"`, 1),
		"bad volume":    strings.Replace(string(b), `"web_db"`, `"../x"`, 1),
		"no hash":       strings.Replace(string(b), strings.Repeat("a", 64), "", 1),
		"version":       strings.Replace(string(b), `"version":1`, `"version":2`, 1),
		"two values":    string(b) + string(b),
	} {
		if _, err := ParseManifest([]byte(raw), "web", m.Archive); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// rig makes backup and volume roots under a temporary folder.
func rig(t *testing.T) Roots {
	t.Helper()
	d := t.TempDir()
	r := Roots{Backups: filepath.Join(d, "backups"), Volumes: filepath.Join(d, "volumes")}
	for _, p := range []string{r.Backups + "/web", r.Volumes + "/web_db/_data/sub"} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunBackup(t *testing.T) {
	r := rig(t)
	p := goodPlan()
	writeJSON(t, r.Backups+"/web/"+PlanFile, p)
	var got []string
	code := 0
	tar := func(_ context.Context, args []string) (int, error) {
		got = args
		if code != 0 {
			return code, os.ErrInvalid
		}
		return 0, nil
	}
	if err := RunBackup(context.Background(), r, "web", tar); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, TarCreate(p)) {
		t.Fatalf("tar %q", got)
	}
	code = 1 // a file changed while read: kept, and marked
	if err := RunBackup(context.Background(), r, "web", tar); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Backups + "/web/" + ChangedName(p.Output)); err != nil {
		t.Fatal("not marked")
	}
	code = 2
	if err := RunBackup(context.Background(), r, "web", tar); err == nil {
		t.Fatal("tar's failure ignored")
	}
	if err := RunBackup(context.Background(), r, "db", tar); err == nil {
		t.Fatal("ran without a plan")
	}
	p.Stack = "db"
	writeJSON(t, r.Backups+"/web/"+PlanFile, p)
	if err := RunBackup(context.Background(), r, "web", tar); err == nil {
		t.Fatal("ran another stack's plan")
	}
	writeJSON(t, r.Backups+"/web/"+PlanFile, map[string]any{"stack": "web", "output": p.Output, "paths": p.Paths, "x": 1})
	if err := RunBackup(context.Background(), r, "web", tar); err == nil {
		t.Fatal("ran a plan with an unknown key")
	}
}

func TestRunRestore(t *testing.T) {
	r := rig(t)
	outside := t.TempDir()
	if err := os.WriteFile(outside+"/keep", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := r.Volumes + "/web_db/_data"
	if err := os.WriteFile(data+"/old", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, data+"/link"); err != nil {
		t.Fatal(err)
	}
	archive := []byte("archive bytes")
	if err := os.WriteFile(r.Backups+"/web/20261106T101500Z.tar.zst", archive, 0o600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(archive)
	p := RestorePlan{Stack: "web", Archive: "20261106T101500Z.tar.zst", SHA256: hex.EncodeToString(h[:]), Volumes: []string{"web_db"}, Secrets: true}
	writeJSON(t, r.Backups+"/web/"+RestoreFile, p)
	var got []string
	tar := func(_ context.Context, args []string) (int, error) { got = args; return 0, nil }
	if err := RunRestore(context.Background(), r, "web", tar); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, TarExtract(p)) {
		t.Fatalf("tar %q", got)
	}
	entries, _ := os.ReadDir(data)
	if len(entries) != 0 {
		t.Fatalf("not emptied: %v", entries)
	}
	if _, err := os.Stat(outside + "/keep"); err != nil {
		t.Fatal("emptying followed a link")
	}
	// A swapped archive: tar never runs.
	got = nil
	if err := os.WriteFile(r.Backups+"/web/20261106T101500Z.tar.zst", []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunRestore(context.Background(), r, "web", tar); err == nil || got != nil {
		t.Fatal("a swapped archive restored")
	}
}

func TestEmptyRefusesLinkedData(t *testing.T) {
	r := rig(t)
	outside := t.TempDir()
	if err := os.WriteFile(outside+"/keep", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.Volumes+"/evil", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, r.Volumes+"/evil/_data"); err != nil {
		t.Fatal(err)
	}
	if err := Empty(r, "evil"); err == nil {
		t.Fatal("emptied a linked _data")
	}
	if err := os.Symlink(outside, r.Volumes+"/evil2"); err != nil {
		t.Fatal(err)
	}
	if err := Empty(r, "evil2"); err == nil {
		t.Fatal("emptied through a linked volume")
	}
	if err := Empty(r, "../x"); err == nil {
		t.Fatal("bad name")
	}
	if _, err := os.Stat(outside + "/keep"); err != nil {
		t.Fatal("a file outside was removed")
	}
}

// FuzzTarArgs: a plan Check accepts gives an argv the validator accepts,
// and an argv the validator accepts never names an option, the backups or
// a path with "..".
func FuzzTarArgs(f *testing.F) {
	f.Add("web", "/home/u/site", "/var/lib/docker/volumes/web_db/_data")
	f.Add("web", "/var/lib/hearth/backups", "/-x")
	f.Add("a", "/var/lib/hearth/stacks/b", "/var/lib/docker/volumes/x/_data/../y")
	f.Fuzz(func(t *testing.T, stack, a, b string) {
		p := Plan{Stack: stack, Output: PartialName("20261106T101500Z"), Paths: []string{StackFolder(stack), a, b}}
		args := TarCreate(p)
		okPlan := p.Check() == nil
		okArgs := CheckTarCreate(args) == nil
		if okPlan && !okArgs {
			t.Fatalf("a valid plan gave refused args: %q", args)
		}
		if !okArgs {
			return
		}
		for _, x := range args[9:] {
			if strings.HasPrefix(x, "-") || strings.HasPrefix(x, "var/lib/hearth/backups") || strings.Contains("/"+x+"/", "/../") {
				t.Fatalf("accepted %q", x)
			}
		}
		r := RestorePlan{Stack: stack, Archive: "20261106T101500Z.tar.zst", SHA256: strings.Repeat("a", 64), Volumes: []string{a}, Secrets: true}
		if r.Check() == nil && CheckTarExtract(TarExtract(r)) != nil {
			t.Fatalf("restore plan and argv disagree for %q", a)
		}
	})
}
