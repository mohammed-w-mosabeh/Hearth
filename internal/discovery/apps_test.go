package discovery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseDesktop(t *testing.T) {
	e, ok := parseDesktop([]byte(`# comment
[Desktop Entry]
Type=Application
Name=Text\sEditor
Name[fr]=Éditeur
Comment=Edit text files
Exec=gnome-text-editor %U
Icon=org.gnome.TextEditor
Categories=GNOME;GTK;Utility;TextEditor;
Keywords=text;plain\;old;
NoDisplay=false
X-Flatpak=org.gnome.TextEditor

[Desktop Action new-window]
Name=New Window
Exec=other
`))
	if !ok || e.Name != "Text Editor" || e.Exec != "gnome-text-editor %U" || len(e.Categories) != 4 || e.Categories[2] != "Utility" ||
		len(e.Keywords) != 2 || e.Keywords[1] != "plain;old" || e.FlatpakID != "org.gnome.TextEditor" {
		t.Fatalf("%+v %v", e, ok)
	}
	if _, ok := parseDesktop([]byte("Name=x\n")); ok {
		t.Fatal("no [Desktop Entry] accepted")
	}
	if e, _ := parseDesktop([]byte("[Desktop Entry]\nName[en]=Fallback\n")); e.Name != "Fallback" {
		t.Fatalf("Name[en] fallback: %q", e.Name)
	}
}

type tree struct {
	t    *testing.T
	root string
}

func (tr tree) file(rel, s string, mode os.FileMode) string {
	tr.t.Helper()
	p := filepath.Join(tr.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		tr.t.Fatal(err)
	}
	return p
}

func desktop(name string, extra ...string) string {
	return "[Desktop Entry]\nType=Application\nName=" + name + "\nExec=" + strings.ToLower(name) + "\n" + strings.Join(extra, "\n") + "\n"
}

type pub struct {
	mu sync.Mutex
	n  int
}

func (p *pub) Publish(string, any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	return nil
}

func (p *pub) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func newTree(t *testing.T) (tree, Config) {
	tr := tree{t: t, root: t.TempDir()}
	cfg := Config{
		Home:         filepath.Join(tr.root, "home"),
		DataHome:     filepath.Join(tr.root, "home/.local/share"),
		DataDirs:     []string{filepath.Join(tr.root, "usr/share"), filepath.Join(tr.root, "var/lib/flatpak/exports/share")},
		Desktops:     []string{"GNOME"},
		Path:         []string{filepath.Join(tr.root, "bin")},
		AppImageDirs: []string{filepath.Join(tr.root, "home/Applications")},
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	sys, usr, fp := "usr/share/applications/", "home/.local/share/applications/", "var/lib/flatpak/exports/share/applications/"
	tr.file(sys+"org.gnome.TextEditor.desktop", desktop("TextEditor", "Icon=org.gnome.TextEditor", "Categories=Utility;"), 0o600)
	tr.file(sys+"firefox.desktop", desktop("Firefox", "Icon=firefox"), 0o600)
	tr.file(usr+"firefox.desktop", desktop("Firefox (mine)"), 0o600) // user file wins
	tr.file(sys+"cups.desktop", desktop("Printers"), 0o600)
	tr.file(usr+"cups.desktop", "[Desktop Entry]\nHidden=true\n", 0o600) // user deletes it
	tr.file(sys+"hidden.desktop", desktop("Hidden", "NoDisplay=true"), 0o600)
	tr.file(sys+"kde-only.desktop", desktop("Konsole", "OnlyShowIn=KDE;"), 0o600)
	tr.file(sys+"not-gnome.desktop", desktop("NotGnome", "NotShowIn=GNOME;"), 0o600)
	tr.file(sys+"tryexec.desktop", desktop("Missing", "TryExec=not-installed"), 0o600)
	tr.file(sys+"tryexec-ok.desktop", desktop("Present", "TryExec=present"), 0o600)
	tr.file("bin/present", "#!/bin/true\n", 0o700)
	tr.file(sys+"link.desktop", "[Desktop Entry]\nType=Link\nName=Web\nURL=x\n", 0o600)
	tr.file(sys+"kde4/dolphin.desktop", desktop("Dolphin"), 0o600) // ID kde4-dolphin
	tr.file(fp+"app.zen_browser.zen.desktop", desktop("Zen", "X-Flatpak=app.zen_browser.zen", "StartupWMClass=zen"), 0o600)
	tr.file("usr/share/icons/hicolor/48x48/apps/org.gnome.TextEditor.png", "small", 0o600)
	tr.file("usr/share/icons/hicolor/128x128/apps/org.gnome.TextEditor.png", "big", 0o600)
	tr.file("usr/share/icons/hicolor/scalable/apps/firefox.svg", "<svg/>", 0o600)
	tr.file("home/Applications/Obsidian-1.5.3.AppImage", "ELF", 0o700)
	tr.file("home/Applications/notes.txt", "x", 0o600)
	return tr, cfg
}

func TestScan(t *testing.T) {
	_, cfg := newTree(t)
	x := NewIndex(cfg)
	if !x.Scan() {
		t.Fatal("first scan reported no change")
	}
	got := map[string]App{}
	for _, a := range x.Apps() {
		got[a.ID] = a
	}
	want := map[string]string{
		"org.gnome.TextEditor": SourceSystem, "firefox": SourceUser, "tryexec-ok": SourceSystem,
		"kde4-dolphin": SourceSystem, "app.zen_browser.zen": SourceFlatpak, "appimage.obsidian-1.5.3": SourceAppImage,
	}
	for id, src := range want {
		if got[id].Source != src {
			t.Errorf("%s: source %q, want %q", id, got[id].Source, src)
		}
	}
	if len(got) != len(want) {
		t.Errorf("apps %v", keys(got))
	}
	if got["firefox"].Name != "Firefox (mine)" || got["firefox"].HasIcon {
		t.Errorf("user override: %+v", got["firefox"])
	}
	if a := got["org.gnome.TextEditor"]; !a.HasIcon || !strings.HasSuffix(a.iconPath, "128x128/apps/org.gnome.TextEditor.png") {
		t.Errorf("icon size preference: %+v", a)
	}
	if d := x.IconData("org.gnome.TextEditor"); d != "data:image/png;base64,Ymln" {
		t.Errorf("icon data %q", d)
	}
	if got["appimage.obsidian-1.5.3"].Name != "Obsidian" {
		t.Errorf("appimage name %q", got["appimage.obsidian-1.5.3"].Name)
	}
	if n, ok := x.Name("ZEN"); !ok || n != "Zen" { // by WM class, any case
		t.Errorf("Name(zen) = %q %v", n, ok)
	}
	if x.Scan() {
		t.Error("unchanged rescan reported a change")
	}
}

func keys(m map[string]App) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestWatch(t *testing.T) {
	tr, cfg := newTree(t)
	p := &pub{}
	cfg.Pub = p
	x := NewIndex(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { x.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for len(x.Apps()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // watcher set up after the first scan
	tr.file("usr/share/applications/new.desktop", desktop("NewApp"), 0o600)
	for time.Now().Before(deadline) {
		if _, ok := x.App("new"); ok && p.count() == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("new app not picked up (published %d)", p.count())
}

func TestRealSystem(t *testing.T) {
	x := NewIndex(ConfigFromEnv(os.Getenv))
	start := time.Now()
	x.Scan()
	took := time.Since(start)
	src := map[string]int{}
	icons := 0
	for _, a := range x.Apps() {
		src[a.Source]++
		if a.HasIcon {
			icons++
		}
	}
	t.Logf("%d apps %v, %d with icons, scan %v", len(x.Apps()), src, icons, took)
}

func TestHandleIcons(t *testing.T) {
	_, cfg := newTree(t)
	x := NewIndex(cfg)
	x.Scan()
	get := func(q string) (int, map[string]string) {
		rec := httptest.NewRecorder()
		x.HandleIcons(rec, httptest.NewRequest(http.MethodGet, "/api/v1/apps/icons"+q, nil))
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	c, got := get("?id=org.gnome.TextEditor&id=firefox&id=../../etc/passwd&id=kde4-dolphin")
	if c != 200 || len(got) != 1 || !strings.HasPrefix(got["org.gnome.TextEditor"], "data:image/png;base64,") {
		t.Fatalf("%d %v", c, got)
	}
	if c, _ := get(""); c != 400 {
		t.Fatalf("no ids: %d", c)
	}
	if c, _ := get("?" + strings.Repeat("id=a&", 61)); c != 400 {
		t.Fatalf("too many ids: %d", c)
	}
}

func TestRunning(t *testing.T) {
	_, cfg := newTree(t)
	x := NewIndex(cfg)
	x.Scan()
	got := x.Running([]string{"org.gnome.TextEditor", "zen", "app.zen_browser.zen", "unknown"})
	if len(got) != 2 || len(got["org.gnome.TextEditor"]) != 1 || strings.Join(got["app.zen_browser.zen"], ",") != "zen,app.zen_browser.zen" {
		t.Fatalf("%v", got)
	}
	rec := httptest.NewRecorder()
	x.HandleRunning(func() []string { return []string{"zen", "firefox"} })(rec, httptest.NewRequest("GET", "/api/v1/apps/running", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"running":["app.zen_browser.zen","firefox"]}` {
		t.Fatalf("body %s", body)
	}
}

func TestValidID(t *testing.T) {
	for s, ok := range map[string]bool{
		"org.gnome.TextEditor": true, "firefox-esr": true, "appimage.obsidian-1.5.3": true, "kde4-dolphin": true,
		"": false, "-x": false, ".x": false, "a/b": false, "a b": false, "a\n": false, strings.Repeat("a", 201): false,
	} {
		if ValidID(s) != ok {
			t.Errorf("%q: %v", s, !ok)
		}
	}
}
