package pkgs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/discovery"
	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
)

// fakeManager plays the systemd user manager: a transient unit "runs" by
// writing output to its log file and ending with result.
type fakeManager struct {
	mu     sync.Mutex
	result string
	output string
	units  []map[string]any
	ch     chan *dbus.Signal
}

func (m *fakeManager) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	return nil, nil
}
func (m *fakeManager) Set(context.Context, string, dbus.ObjectPath, string, string, any) error {
	return nil
}
func (m *fakeManager) Close() error { return nil }
func (m *fakeManager) Watch(context.Context, ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	m.ch = make(chan *dbus.Signal, 4)
	return m.ch, nil
}

func (m *fakeManager) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	if method != sdManager+".StartTransientUnit" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	props := map[string]any{"unit": args[0]}
	for _, p := range args[2].([]unitProp) {
		props[p.Name] = p.Value.Value()
	}
	m.units = append(m.units, props)
	_ = os.WriteFile(props["StandardOutputFileToTruncate"].(string), []byte(m.output), 0o600)
	job := dbus.ObjectPath("/org/freedesktop/systemd1/job/7")
	*(out[0].(*dbus.ObjectPath)) = job
	m.ch <- &dbus.Signal{Body: []any{uint32(1), dbus.ObjectPath("/other"), "x.service", "done"}}
	m.ch <- &dbus.Signal{Body: []any{uint32(7), job, args[0], m.result}}
	return nil
}

func runner(t *testing.T, m *fakeManager) *UserRunner {
	return &UserRunner{Bus: func(context.Context) (Bus, error) { return m, nil }, LogDir: filepath.Join(t.TempDir(), "runs")}
}

func TestUserRunner(t *testing.T) {
	m := &fakeManager{result: "done", output: "Uninstalled.\n"}
	u := runner(t, m)
	out, err := u.Run(context.Background(), "Remove x", "/usr/bin/flatpak", []string{"flatpak", "uninstall"}, []string{"HOME=/h"})
	if err != nil || out != "Uninstalled." {
		t.Fatalf("%q %v", out, err)
	}
	p := m.units[0]
	ex := p["ExecStartEx"].([]execEx)[0]
	if p["Type"] != "oneshot" || ex.Path != "/usr/bin/flatpak" || strings.Join(ex.Argv, " ") != "flatpak uninstall" ||
		ex.Flags[0] != "no-env-expand" || !strings.HasPrefix(p["unit"].(string), "hearth-pkg-") {
		t.Fatalf("%+v", p)
	}
	if left, _ := os.ReadDir(u.LogDir); len(left) != 0 {
		t.Errorf("log left behind: %v", left)
	}
	m.result, m.output = "failed", "error: app/x not installed"
	var re *RunError
	if _, err := u.Run(context.Background(), "Remove x", "/usr/bin/flatpak", []string{"flatpak"}, nil); err == nil ||
		err.Error() != "error: app/x not installed" || !errors.As(err, &re) || re.Result != "failed" {
		t.Fatalf("%v", err)
	}
	if _, err := u.Run(context.Background(), "x", "flatpak", []string{"flatpak"}, nil); err == nil {
		t.Error("relative path accepted")
	}
}

func TestExtraPlansAndCommands(t *testing.T) {
	home, sys := t.TempDir(), t.TempDir()
	mk := func(path, content string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o750)
		_ = os.WriteFile(path, []byte(content), 0o600)
	}
	for _, r := range []struct{ kind, id, branch, meta string }{
		{"app", "org.example.Editor", "stable", "[Application]\nruntime=org.example.Platform/x86_64/1\n"},
		{"runtime", "org.example.Platform", "1", "[Runtime]\n"},
	} {
		dir := filepath.Join(sys, r.kind, r.id, "x86_64", r.branch)
		mk(filepath.Join(dir, "d/metadata"), r.meta)
		_ = os.Symlink("d", filepath.Join(dir, "active"))
	}
	prefix := filepath.Join(home, ".nvm/versions/node/v22.0.0")
	mk(filepath.Join(prefix, "bin/npm"), "")
	mk(filepath.Join(prefix, "lib/node_modules/@scope/tool/package.json"), `{"version":"1.0.0"}`)

	m := &fakeManager{result: "done"}
	cfg := ExtraConfig{Find: discovery.ExtraConfig{Home: home, Roots: []string{sys}}, Run: runner(t, m)}
	kinds := map[string]extraKind{}
	for _, k := range extraKinds {
		kinds[k.kind] = k
	}
	ctx := context.Background()

	p, err := extraPlan(ctx, cfg, kinds["flatpak"], "system/runtime/org.example.Platform/x86_64/1")
	if err != nil || len(p.Items) != 0 || len(p.Blocked) != 1 || p.Blocked[0].Reason != discovery.WhyInUse || p.Blocked[0].Detail != "org.example.Editor" {
		t.Fatalf("runtime in use: %v %+v", err, p)
	}
	p, err = extraPlan(ctx, cfg, kinds["flatpak"], "system/app/org.example.Editor/x86_64/stable")
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("%v %+v", err, p)
	}
	if res, err := extraRun(ctx, cfg, "flatpak", p); err != nil || res.Done != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if ex := m.units[0]["ExecStartEx"].([]execEx)[0]; strings.Join(ex.Argv, " ") != "flatpak uninstall -y --system app/org.example.Editor/x86_64/stable" {
		t.Errorf("flatpak argv %v", ex.Argv)
	}

	p, err = extraPlan(ctx, cfg, kinds["npm"], prefix+"::@scope/tool")
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("%v %+v", err, p)
	}
	m.result, m.output = "failed", "npm error code EACCES"
	res, err := extraRun(ctx, cfg, "npm", p)
	if err != nil || res.Done != 0 || len(res.Failed) != 1 || res.Failed[0].Detail != "npm error code EACCES" {
		t.Fatalf("%v %+v", err, res)
	}
	u := m.units[1]
	if ex := u["ExecStartEx"].([]execEx)[0]; ex.Path != prefix+"/bin/npm" || strings.Join(ex.Argv, " ") != "npm uninstall --global @scope/tool" {
		t.Errorf("npm command %+v", ex)
	}
	if env := strings.Join(u["Environment"].([]string), " "); !strings.Contains(env, "PATH="+prefix+"/bin:") {
		t.Errorf("npm env %s", env)
	}

	if _, err := extraPlan(ctx, cfg, kinds["npm"], prefix+"::absent"); status(err) != http.StatusNotFound {
		t.Errorf("absent: %v", err)
	}
	for kind, bad := range map[string]string{
		"flatpak": "system/app/../x86_64/stable", "npm": "relative::x", "snap": "Bad", "cargo": "-x", "gobin": "../x",
	} {
		if kinds[kind].valid(bad) == nil {
			t.Errorf("%s target %q accepted", kind, bad)
		}
	}
	path, argv, _ := command(discovery.ExtraConfig{Home: home}, "gobin", discovery.ExtraPackage{Name: "x", Location: "/h/go/bin/x"})
	if path != "/usr/bin/gio" || strings.Join(argv, " ") != "gio trash -- /h/go/bin/x" {
		t.Errorf("gobin %s %v", path, argv)
	}
}

func TestRemoveSnap(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "snapd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	polls := 0
	fail := false
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(b)+" "+r.Header.Get("X-Allow-Interaction"))
		switch {
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"type":"async","status-code":202,"change":"42"}`)
		case polls < 1:
			polls++
			_, _ = io.WriteString(w, `{"result":{"ready":false,"status":"Doing"}}`)
		case fail:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"ready": true, "status": "Error", "err": "cannot remove: in use"}})
		default:
			_, _ = io.WriteString(w, `{"result":{"ready":true,"status":"Done"}}`)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	cfg := ExtraConfig{Find: discovery.ExtraConfig{SnapSocket: sock}, SnapPoll: 1}
	if err := removeSnap(context.Background(), cfg, "firefox"); err != nil {
		t.Fatal(err)
	}
	if seen[0] != `POST /v2/snaps/firefox {"action":"remove"} true` || seen[1] != "GET /v2/changes/42  " || len(seen) != 3 {
		t.Fatalf("%q", seen)
	}
	fail = true
	if err := removeSnap(context.Background(), cfg, "firefox"); err == nil || err.Error() != "cannot remove: in use" {
		t.Fatalf("%v", err)
	}
}

func FuzzRemoveTargets(f *testing.F) {
	for _, s := range []string{"system/app/org.gnome.Maps/x86_64/stable", "user/runtime/-x/a/b", "system/app/../x86_64/stable",
		"/home/me/.npm-global::@scope/pkg", "rel::x", "/a/../b::x", "/p::-x", "/p::a b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		unsafe := func(p string) bool {
			return p == "" || strings.HasPrefix(p, "-") || strings.HasPrefix(p, ".") || strings.ContainsAny(p, " \t\n\r\x00;$`'\"\\")
		}
		if validFlatpak(s) == nil {
			f := strings.Split(s, "/")
			if len(f) != 5 || unsafe(f[2]) || unsafe(f[3]) || unsafe(f[4]) {
				t.Fatalf("flatpak accepted %q", s)
			}
		}
		if validNpm(s) == nil {
			prefix, name, _ := strings.Cut(s, "::")
			if !filepath.IsAbs(prefix) || strings.Contains(prefix+"/", "/../") || unsafe(name) || strings.Count(name, "/") > 1 ||
				(strings.Contains(name, "/") && !strings.HasPrefix(name, "@")) {
				t.Fatalf("npm accepted %q", s)
			}
		}
	})
}

// Removing a snap or a system Flatpak asks polkit: unattended, it is
// refused before anything runs (S15d).
func TestUnattendedExtraRemovalsRefused(t *testing.T) {
	ctx := unattended.With(context.Background())
	for _, c := range [][2]string{{"flatpak", "system/app/org.x.Y/x86_64/stable"}} {
		if err := RemoveExtra(ctx, ExtraConfig{}, c[0], c[1]); !errors.Is(err, unattended.ErrWouldPrompt) {
			t.Errorf("%v: %v", c, err)
		}
	}
	if err := removeSnap(ctx, ExtraConfig{}, "firefox"); !errors.Is(err, unattended.ErrWouldPrompt) {
		t.Errorf("snap: %v", err)
	}
}
