package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func tokenInFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a file the test made
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestTokenLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tokens")
	tk, skipped, err := NewTokens(dir)
	if err != nil || len(skipped) > 0 {
		t.Fatal(err, skipped)
	}
	info, err := tk.Create("cli")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(info.File)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o, want 700", di.Mode().Perm())
	}
	tok := tokenInFile(t, info.File)
	if !tk.Valid(tok) {
		t.Fatal("a new token is not valid")
	}
	if tk.Valid(tok+"x") || tk.Valid("") || tk.Valid(tok[len(tokenPrefix):]) {
		t.Error("something else was accepted")
	}
	if _, err := tk.Create("cli"); !errors.Is(err, ErrTokenExists) {
		t.Errorf("second cli: %v", err)
	}

	// A restart loads what the files say.
	again, skipped, err := NewTokens(dir)
	if err != nil || len(skipped) > 0 {
		t.Fatal(err, skipped)
	}
	if !again.Valid(tok) {
		t.Error("token lost across a restart")
	}
	if l := again.List(); len(l) != 1 || l[0].Name != "cli" || l[0].File != info.File || l[0].Created == 0 {
		t.Errorf("list: %+v", l)
	}

	if err := again.Revoke("cli"); err != nil {
		t.Fatal(err)
	}
	if again.Valid(tok) {
		t.Error("revoked token still valid")
	}
	if _, err := os.Stat(info.File); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file left behind: %v", err)
	}
	if err := again.Revoke("cli"); !errors.Is(err, ErrNoToken) {
		t.Errorf("second revoke: %v", err)
	}
	third, _, err := NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	if third.Valid(tok) {
		t.Error("revoked token came back after a restart")
	}
}

func TestTokensIgnoreFilesThatAreNotPrivateTokens(t *testing.T) {
	dir := t.TempDir()
	good := tokenPrefix + strings.Repeat("a", 43)
	if err := os.WriteFile(filepath.Join(dir, "wide.token"), []byte(good), 0o644); err != nil { // #nosec G306 -- the case under test
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "short.token"), []byte("hearth_ro_x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.token")); err != nil {
		t.Fatal(err)
	}
	tk, skipped, err := NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 3 {
		t.Errorf("skipped %v, want the wide, short and link files", skipped)
	}
	if tk.Valid(good) || len(tk.List()) != 0 {
		t.Error("an unsafe file was loaded")
	}
	// Create never replaces a file it did not load.
	if _, err := tk.Create("wide"); !errors.Is(err, ErrTokenExists) {
		t.Errorf("create over a skipped file: %v", err)
	}
}

func TestTokenLimitAndNames(t *testing.T) {
	tk, _, err := NewTokens(filepath.Join(t.TempDir(), "tokens"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "-a", "A", "a b", "a/b", "..", "a.b", strings.Repeat("a", 33), "ä"} {
		if _, err := tk.Create(bad); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	for i := range MaxTokens {
		if _, err := tk.Create("t" + strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tk.Create("one-more"); !errors.Is(err, ErrTokenLimit) {
		t.Errorf("over the limit: %v", err)
	}
}

func TestNilTokensAcceptNothing(t *testing.T) {
	var tk *Tokens
	if tk.Valid(tokenPrefix+strings.Repeat("a", 43)) || len(tk.List()) != 0 {
		t.Error("a nil Tokens accepted something")
	}
}

func FuzzValidTokenName(f *testing.F) {
	for _, s := range []string{"cli", "a-b", "../x", "a/b", "a\x00", ".", "-x", "A"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if !ValidTokenName(name) {
			return
		}
		if len(name) > 32 || filepath.Base(name) != name || strings.ContainsAny(name, "./\\\x00\n") || name[0] == '-' {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	})
}
