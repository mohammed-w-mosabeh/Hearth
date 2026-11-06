package broker

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testSpec = `{"version":1,"name":"web","services":{"app":{"image":"nginx:1.27","environment":{"A":"$x"},"secrets":["PW"],
"ports":[{"host":8080,"container":80}],"volumes":[{"type":"bind","source":"/srv/site","target":"/usr/share/nginx/html","read_only":true}]}},
"bind_roots":["/srv/site"]}`

func TestStackPrepare(t *testing.T) {
	st := StackStore{Dir: filepath.Join(t.TempDir(), "stacks")}
	sp, err := st.Prepare([]byte(testSpec), "up")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(st.Dir, sp.Name)
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder %v %v", fi, err)
	}
	for _, f := range []string{FileSpec, FileCompose, FileSecrets, FileArgs} {
		fi, err := os.Lstat(filepath.Join(dir, f))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, fi, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 4 {
		t.Fatalf("left over: %v", entries)
	}
	args, _ := st.Read("web", FileArgs)
	if string(args) != "STACK_ARGS="+StackVerbs["up"]+"\n" {
		t.Fatalf("args %q", args)
	}
	compose, _ := st.Read("web", FileCompose)
	if !strings.Contains(string(compose), `"A":"$$x"`) || !strings.Contains(string(compose), `"PW":"${PW}"`) {
		t.Fatalf("compose %s", compose)
	}
	// The secrets file is never overwritten by a new deploy.
	if err := os.WriteFile(filepath.Join(dir, FileSecrets), []byte("PW=kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Prepare([]byte(testSpec), "update"); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.Read("web", FileSecrets); string(b) != "PW=kept\n" {
		t.Fatalf("secrets %q", b)
	}
	if b, _ := st.Read("web", FileArgs); !strings.Contains(string(b), "--pull always") {
		t.Fatalf("args %q", b)
	}
}

func TestStackPrepareRefuses(t *testing.T) {
	st := StackStore{Dir: filepath.Join(t.TempDir(), "stacks")}
	bad := strings.Replace(testSpec, `"/srv/site"]`, `"/etc"]`, 1)
	for name, c := range map[string]struct{ raw, verb string }{
		"refused spec": {bad, "up"},
		"yaml":         {"name: web\n", "up"},
		"verb":         {testSpec, "exec"},
		"verb flags":   {testSpec, "up --build"},
	} {
		if _, err := st.Prepare([]byte(c.raw), c.verb); err == nil {
			t.Errorf("%s: written", name)
		}
	}
	if _, err := os.Stat(st.Dir); err == nil {
		if e, _ := os.ReadDir(st.Dir); len(e) != 0 {
			t.Fatalf("something was written: %v", e)
		}
	}
}

func TestStackStoreLinks(t *testing.T) {
	base := t.TempDir()
	st := StackStore{Dir: filepath.Join(base, "stacks")}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(st.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The stack folder is a link out.
	if err := os.Symlink(outside, filepath.Join(st.Dir, "web")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Prepare([]byte(testSpec), "up"); err == nil {
		t.Fatal("wrote through a linked folder")
	}
	if e, _ := os.ReadDir(outside); len(e) != 0 {
		t.Fatalf("wrote outside: %v", e)
	}
	// A file in the folder is a link out: it is replaced, not followed.
	if err := os.Remove(filepath.Join(st.Dir, "web")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Prepare([]byte(testSpec), "up"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "victim")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(st.Dir, "web", FileCompose)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Prepare([]byte(testSpec), "up"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target) // #nosec G304 -- a test file
	if string(b) != "untouched" {
		t.Fatal("followed a link")
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the link is still there")
	}
	// Reading through a link out is refused by os.Root.
	if err := os.Remove(filepath.Join(st.Dir, "web", FileArgs)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(st.Dir, "web", FileArgs)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Read("web", FileArgs); err == nil {
		t.Fatal("read through a link out")
	}
}

func TestStackStoreNames(t *testing.T) {
	st := StackStore{Dir: t.TempDir()}
	for _, n := range []string{"", "../x", "-x", "a/b", ".", "..", "WEB", "wеb"} {
		if err := st.Write(n, map[string][]byte{FileArgs: nil}); err == nil {
			t.Errorf("%q written", n)
		}
	}
	for _, f := range []string{"../x", "other", ".spec.json", "spec.json/"} {
		if err := st.Write("web", map[string][]byte{f: nil}); err == nil {
			t.Errorf("file %q written", f)
		}
	}
	if err := st.SetVerb("web", "up"); err == nil {
		t.Fatal("verb set on a missing stack")
	}
}

func TestStackVerbsAreFixed(t *testing.T) {
	want := []string{"create", "down", "down-volumes", "restart", "start", "stop", "up", "update"}
	if got := slices.Sorted(maps.Keys(StackVerbs)); !slices.Equal(got, want) {
		t.Fatalf("verbs %v", got)
	}
	for verb, args := range StackVerbs {
		for _, bad := range []string{"--volumes", "-v", "--build", "build", "exec", "run", "rm", "-y", "--yes", "--force-recreate"} {
			if bad == "--volumes" && verb == "down-volumes" {
				continue // the one verb that deletes volumes, after a typed name
			}
			if slices.Contains(strings.Fields(args), bad) {
				t.Errorf("%s runs %s", verb, bad)
			}
		}
	}
}

func TestStackValidate(t *testing.T) {
	m := StackMethods()[0]
	if m.Name != "stack.validate" || m.Privileged {
		t.Fatalf("%s privileged=%v", m.Name, m.Privileged)
	}
	call := func(p string) (any, error) { return m.Handle(context.Background(), Caller{}, json.RawMessage(p)) }
	got, err := call(`{"spec":` + testSpec + `}`)
	if err != nil {
		t.Fatal(err)
	}
	if r := got.(StackValidateResult); len(r.Refused) != 0 || len(r.Flags) != 0 {
		t.Fatalf("%+v", r)
	}
	got, err = call(`{"spec":` + strings.Replace(testSpec, `"host":8080`, `"host_ip":"0.0.0.0","host":8080`, 1) + `}`)
	if err != nil {
		t.Fatal(err)
	}
	if r := got.(StackValidateResult); len(r.Refused) != 1 || !slices.Equal(r.Flags, []string{"port:0.0.0.0:8080/tcp"}) {
		t.Fatalf("%+v", r)
	}
	for _, p := range []string{``, `{}`, `{"spec":"name: web"}`, `{"spec":{"Version":1}}`, `{"spec":{},"extra":1}`} {
		if _, err := call(p); code(err) != CodeBadParams {
			t.Errorf("%s: %v", p, err)
		}
	}
}
