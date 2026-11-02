package integrity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

type fakeStore struct {
	base     map[string]store.IntegrityEntry
	text     map[string]*string
	settings map[string]json.RawMessage
}

func (s *fakeStore) IntegrityBaseline(context.Context) (map[string]store.IntegrityEntry, error) {
	out := map[string]store.IntegrityEntry{}
	for k, v := range s.base {
		v.Text = nil
		out[k] = v
	}
	return out, nil
}
func (s *fakeStore) IntegrityText(_ context.Context, p string) (*string, error) {
	return s.text[p], nil
}
func (s *fakeStore) PutIntegrity(_ context.Context, e store.IntegrityEntry) error {
	s.base[e.Path], s.text[e.Path] = e, e.Text
	return nil
}
func (s *fakeStore) DeleteIntegrity(_ context.Context, p string) error {
	delete(s.base, p)
	delete(s.text, p)
	return nil
}
func (s *fakeStore) Setting(_ context.Context, k string) (json.RawMessage, bool, error) {
	v, ok := s.settings[k]
	return v, ok, nil
}
func (s *fakeStore) SetSetting(_ context.Context, k string, v json.RawMessage) error {
	s.settings[k] = v
	return nil
}

type fakeAudit struct{ got []store.AuditEntry }

func (a *fakeAudit) Audit(_ context.Context, e store.AuditEntry) error {
	a.got = append(a.got, e)
	return nil
}

type harness struct {
	m     *Manager
	home  string
	sys   string
	st    *fakeStore
	audit *fakeAudit
	seen  []map[string]float64
}

func setup(t *testing.T) *harness {
	t.Helper()
	h := &harness{home: t.TempDir(), sys: t.TempDir(), audit: &fakeAudit{},
		st: &fakeStore{base: map[string]store.IntegrityEntry{}, text: map[string]*string{}, settings: map[string]json.RawMessage{}}}
	h.m = New(Config{Store: h.st, Audit: h.audit, Home: h.home, SystemDirs: []string{h.sys},
		Observe: func(_ context.Context, _ time.Time, v map[string]float64) { h.seen = append(h.seen, v) }})
	return h
}

func (h *harness) write(t *testing.T, name, body string) {
	t.Helper()
	p := filepath.Join(h.home, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) changes(t *testing.T) map[string]Change {
	t.Helper()
	v := h.m.view(context.Background())
	if v.Error != "" {
		t.Fatal(v.Error)
	}
	out := map[string]Change{}
	for _, c := range v.Changes {
		out[c.Path] = c
	}
	return out
}

func TestFirstScanIsTheBaselineThenChangesShow(t *testing.T) {
	h := setup(t)
	h.write(t, ".bashrc", "alias ll='ls -l'\n")
	h.write(t, ".ssh/authorized_keys", "ssh-ed25519 AAAA one\n")
	h.write(t, ".local/bin/tool", "#!/bin/sh\n")
	if got := h.changes(t); len(got) != 0 {
		t.Fatalf("the first scan reported %v", got)
	}
	if len(h.seen) != 1 || len(h.seen[0]) != 0 {
		t.Fatalf("the first scan alerted: %v", h.seen)
	}

	h.write(t, ".ssh/authorized_keys", "ssh-ed25519 AAAA one\nssh-ed25519 BBBB two\n")
	h.write(t, ".local/bin/new", "x")
	if err := os.Remove(filepath.Join(h.home, ".bashrc")); err != nil {
		t.Fatal(err)
	}
	got := h.changes(t)
	if got["~/.ssh/authorized_keys"].Kind != "changed" || got["~/.local/bin/new"].Kind != "added" || got["~/.bashrc"].Kind != "removed" {
		t.Fatalf("changes: %+v", got)
	}
	last := h.seen[len(h.seen)-1]
	if last["~/.ssh/authorized_keys"] != 1 || last["~/.local/bin"] != 1 || last["~/.bashrc"] != 1 || len(last) != 3 {
		t.Fatalf("instances: %v", last)
	}
}

func TestTouchedButSameIsNoChange(t *testing.T) {
	h := setup(t)
	h.write(t, ".profile", "same\n")
	h.changes(t)
	h.write(t, ".profile", "same\n")
	if got := h.changes(t); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestRewriteWithTheOldMtimeIsSeen(t *testing.T) {
	h := setup(t)
	h.write(t, ".profile", "before\n")
	p := filepath.Join(h.home, ".profile")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	h.changes(t)
	time.Sleep(10 * time.Millisecond) // past the filesystem's clock tick
	h.write(t, ".profile", "after!\n"[:7])
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := h.changes(t); got["~/.profile"].Kind != "changed" {
		t.Fatalf("a forged mtime hid a change: %v", got)
	}
}

func TestLinksAreNotFollowed(t *testing.T) {
	h := setup(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("TOP\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.home, ".local/bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h.home, ".local/bin/link")); err != nil {
		t.Fatal(err)
	}
	h.changes(t)
	if h.st.base["~/.local/bin/link"].Kind != KindLink {
		t.Fatalf("%+v", h.st.base)
	}
	// The target's content is not watched; where the link points is.
	if err := os.WriteFile(outside, []byte("OTHER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := h.changes(t); len(got) != 0 {
		t.Fatalf("followed the link: %v", got)
	}
	if err := os.Remove(filepath.Join(h.home, ".local/bin/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(h.home, ".local/bin/link")); err != nil {
		t.Fatal(err)
	}
	if got := h.changes(t); got["~/.local/bin/link"].Kind != "changed" {
		t.Fatalf("a retargeted link went unseen: %v", got)
	}
}

func TestLinkedParentCannotLeaveTheHome(t *testing.T) {
	h := setup(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "authorized_keys"), []byte("out\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h.home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	h.changes(t)
	if e, ok := h.st.base["~/.ssh/authorized_keys"]; ok && e.Hash != "" {
		t.Fatalf("read through a link out of the home: %+v", e)
	}
}

func TestSystemNames(t *testing.T) {
	h := setup(t)
	if err := os.WriteFile(filepath.Join(h.sys, "job"), []byte("* * * * * root x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := h.m.view(context.Background())
	if len(v.System) != 1 || !v.System[0].Readable || len(v.System[0].Names) != 1 || v.System[0].Names[0] != "job" {
		t.Fatalf("%+v", v.System)
	}
	if err := os.WriteFile(filepath.Join(h.sys, "job"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := h.changes(t); len(got) != 0 {
		t.Fatalf("a name's content counted: %v", got)
	}
	if err := os.WriteFile(filepath.Join(h.sys, "evil"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := h.changes(t); got[h.sys+"/evil"].Kind != "added" {
		t.Fatalf("%v", got)
	}
}

func TestUnreadableFolderIsNotRemoved(t *testing.T) {
	h := setup(t)
	if err := os.WriteFile(filepath.Join(h.sys, "job"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.changes(t)
	if err := os.Chmod(h.sys, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.sys, 0o700) }) // #nosec G302 -- a test directory, restored so it can be removed
	if _, err := os.ReadDir(h.sys); err == nil {
		t.Skip("running as root")
	}
	v := h.m.view(context.Background())
	if len(v.Changes) != 0 || v.System[0].Readable {
		t.Fatalf("%+v", v)
	}
}

func TestAcceptNeedsWhatThePageShowed(t *testing.T) {
	h := setup(t)
	h.write(t, ".zshrc", "one\n")
	h.changes(t)
	h.write(t, ".zshrc", "two\n")
	c := h.changes(t)["~/.zshrc"]
	h.write(t, ".zshrc", "three, after the page drew\n")
	n, err := h.m.accept(context.Background(), []AcceptItem{{Path: c.Path, Hash: c.Hash}})
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(h.audit.got) != 0 {
		t.Fatalf("audited what it did not do: %+v", h.audit.got)
	}
	c = h.changes(t)["~/.zshrc"]
	n, err = h.m.accept(context.Background(), []AcceptItem{{Path: c.Path, Hash: c.Hash}})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := h.changes(t); len(got) != 0 {
		t.Fatalf("still pending: %v", got)
	}
	if len(h.audit.got) != 1 || h.audit.got[0].Action != "security.integrity.accept" {
		t.Fatalf("%+v", h.audit.got)
	}
	if last := h.seen[len(h.seen)-1]; len(last) != 0 {
		t.Fatalf("the incident did not end: %v", last)
	}
}

func TestAcceptRemoved(t *testing.T) {
	h := setup(t)
	h.write(t, ".zshrc", "one\n")
	h.changes(t)
	if err := os.Remove(filepath.Join(h.home, ".zshrc")); err != nil {
		t.Fatal(err)
	}
	n, err := h.m.accept(context.Background(), []AcceptItem{{Path: "~/.zshrc"}})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := h.changes(t); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestLargeFileKeepsItsHashOnceSettled(t *testing.T) {
	a := store.IntegrityEntry{Kind: KindFile, Size: 1, MTime: 5, CTime: 1_000_000, Accepted: 1 + racyWindow.Milliseconds()}
	if sameStat(a, 1, 5, 1_000_000) {
		t.Fatal("trusted a file touched as it was accepted")
	}
	a.Accepted += 1000
	if !sameStat(a, 1, 5, 1_000_000) {
		t.Fatal("a settled file was read again")
	}
	if sameStat(a, 1, 5, 2_000_000) {
		t.Fatal("ignored a new ctime")
	}
}

func TestDiffOnlyForWatchedPaths(t *testing.T) {
	h := setup(t)
	h.write(t, ".bashrc", "a\n")
	h.write(t, "notes.txt", "private\n")
	h.changes(t)
	for _, p := range []string{"~/notes.txt", "/etc/passwd", "~/../../etc/passwd", "~/.bashrc/../notes.txt"} {
		if _, ok, err := h.m.diff(context.Background(), p); ok || err != nil {
			t.Fatalf("%q: ok=%v err=%v", p, ok, err)
		}
	}
}

func TestDiffMasksAndSaysWhenItCannotShow(t *testing.T) {
	h := setup(t)
	h.write(t, ".bashrc", "alias a=b\nexport API_TOKEN=hunter2\n")
	h.changes(t)
	if strings.Contains(*h.st.text["~/.bashrc"], "hunter2") {
		t.Fatal("a secret reached the store")
	}
	h.write(t, ".bashrc", "alias a=b\nexport API_TOKEN=hunter3\n")
	d, ok, err := h.m.diff(context.Background(), "~/.bashrc")
	if err != nil || !ok || d.Note != "hidden" || len(d.Lines) != 0 {
		t.Fatalf("%+v ok=%v err=%v", d, ok, err)
	}
	if got := h.changes(t); got["~/.bashrc"].Kind != "changed" {
		t.Fatalf("a hidden line's change must still show: %v", got)
	}
	h.write(t, ".bashrc", "alias a=c\nexport API_TOKEN=hunter3\n")
	d, _, _ = h.m.diff(context.Background(), "~/.bashrc")
	body, _ := json.Marshal(d)
	if strings.Contains(string(body), "hunter") || !strings.Contains(string(body), "alias a=c") {
		t.Fatalf("%s", body)
	}
}

func TestDiffOfBinaryAndPrivateKeyHasNoText(t *testing.T) {
	h := setup(t)
	h.write(t, ".local/bin/bin", "a\x00b")
	h.write(t, ".local/bin/key", "-----BEGIN OPENSSH PRIVATE KEY-----\nabcdef\n-----END OPENSSH PRIVATE KEY-----\n")
	h.changes(t)
	if h.st.text["~/.local/bin/key"] != nil || h.st.text["~/.local/bin/bin"] != nil {
		t.Fatal("stored text it should not")
	}
	h.write(t, ".local/bin/bin", "a\x00c")
	d, ok, err := h.m.diff(context.Background(), "~/.local/bin/bin")
	if err != nil || !ok || d.Note != "notext" {
		t.Fatalf("%+v", d)
	}
}

func TestPublicKeyLineStaysVisible(t *testing.T) {
	blob := "AAAAC3NzaC1lZDI1NTE5AAAAIkeyxkeyxkeyxkeyxkeyxkeyxkeyxkeyxkeyxkeyxkeyxkey"
	line := "ssh-ed25519 " + blob + " me@laptop"
	if got := maskLine(line); got != line {
		t.Fatalf("got %q", got)
	}
	if got := maskLine("export API_KEY=" + blob); got != Hidden {
		t.Fatalf("got %q", got)
	}
	for _, s := range []string{"password=x", "Passwd: y", "my_secret z", "GH_TOKEN=1", "auth 1", "credentials"} {
		if maskLine(s) != Hidden {
			t.Fatalf("%q was shown", s)
		}
	}
}

func TestDiffAlgorithm(t *testing.T) {
	lines, truncated, visible := Diff("a\nb\nc\n", "a\nx\nc\nd\n")
	if truncated || !visible {
		t.Fatal(truncated, visible)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Op + l.Text + "\n")
	}
	if got := b.String(); got != " a\n-b\n+x\n c\n+d\n" {
		t.Fatalf("got %q", got)
	}
	var long strings.Builder
	for range 500 {
		long.WriteString("x\n")
	}
	lines, _, _ = Diff("start\n"+long.String()+"end\n", "START\n"+long.String()+"END\n")
	gaps := 0
	for _, l := range lines {
		if l.Gap {
			gaps++
		}
	}
	if gaps != 1 || len(lines) > 12 {
		t.Fatalf("context not trimmed: %d lines, %d gaps", len(lines), gaps)
	}
}

func TestRoutes(t *testing.T) {
	h := setup(t)
	h.write(t, ".bashrc", "a\n")
	h.changes(t)
	h.write(t, ".bashrc", "b\n")
	mux := http.NewServeMux()
	h.m.Register(muxAdapter{mux})
	do := func(method, target, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, target, strings.NewReader(body)))
		return w
	}
	if w := do("GET", "/api/v1/security/integrity/diff?path="+url.QueryEscape("/etc/passwd"), ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := do("GET", "/api/v1/security/integrity/diff", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, bad := range []string{`{}`, `{"items":[]}`, `{"all":true}`, `nope`} {
		if w := do("POST", "/api/v1/security/integrity/accept", bad); w.Code != 400 {
			t.Fatalf("%s: %d", bad, w.Code)
		}
	}
	c := h.changes(t)["~/.bashrc"]
	body, _ := json.Marshal(map[string]any{"items": []AcceptItem{{Path: c.Path, Hash: c.Hash}}})
	w := do("POST", "/api/v1/security/integrity/accept", string(body))
	var v View
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Changes) != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

type muxAdapter struct{ m *http.ServeMux }

func (a muxAdapter) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	a.m.HandleFunc(p, h)
}

func FuzzMaskAndDiff(f *testing.F) {
	f.Add("a\nb\n", "a\nc\n")
	f.Add("password=1\n", "")
	f.Add("\x00\xff", "\n\n\n")
	f.Fuzz(func(t *testing.T, a, b string) {
		if len(a) > 4096 || len(b) > 4096 {
			return
		}
		lines, _, _ := Diff(maskText(a), maskText(b))
		for _, l := range lines {
			if len([]rune(l.Text)) > maxLineRunes {
				t.Fatalf("line too long: %d", len(l.Text))
			}
			if secretWord.MatchString(longBlob.ReplaceAllString(l.Text, "")) && l.Text != Hidden && !strings.Contains(l.Text, "�") {
				// Cutting a line to length or dropping a control character
				// can only remove text, except by joining what it split.
				if !strings.ContainsAny(a+b, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\r\x0e\x0f\x7f") {
					t.Fatalf("a secret word was shown: %q", l.Text)
				}
			}
		}
	})
}
