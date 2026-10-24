package safeexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func anyArgs([]string) error { return nil }

func TestRunUnknownCommand(t *testing.T) {
	_, err := Run(context.Background(), "sh", "id")
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("got %v, want ErrUnknownCommand", err)
	}
}

func TestRunRejectsBadArgsBeforeExec(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"file:///etc/passwd"},
		{"http://x", "http://y"},
		{},
	} {
		_, err := Run(context.Background(), "xdg-open", args...)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("xdg-open %q: got %v, want ErrInvalidArg", args, err)
		}
	}
}

func TestRunBaseCheckAppliesToEveryArg(t *testing.T) {
	spec := Spec{Paths: []string{"/bin/true"}, Validate: anyArgs}
	if _, err := run(context.Background(), spec, []string{"ok", "bad\x00"}); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("got %v, want ErrInvalidArg", err)
	}
}

func TestRunSpecWithoutValidator(t *testing.T) {
	if _, err := run(context.Background(), Spec{Paths: []string{"/bin/true"}}, nil); err == nil {
		t.Fatal("spec without validator ran")
	}
}

func TestRunSuccessAndExitCode(t *testing.T) {
	res, err := run(context.Background(), Spec{Paths: []string{"/nonexistent/echo", "/bin/echo"}, Validate: anyArgs}, []string{"hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != "hello\n" || res.ExitCode != 0 {
		t.Fatalf("stdout %q exit %d", got, res.ExitCode)
	}

	res, err = run(context.Background(), Spec{Paths: []string{"/bin/false"}, Validate: anyArgs}, nil)
	if err == nil || res.ExitCode != 1 {
		t.Fatalf("false: err %v exit %d", err, res.ExitCode)
	}
}

func TestRunMinimalEnv(t *testing.T) {
	t.Setenv("HEARTH_TEST_SECRET_TOKEN", "leak")
	t.Setenv("HEARTH_TEST_PASS", "passed")
	spec := Spec{Paths: []string{"/usr/bin/env"}, Validate: anyArgs, Env: []string{"HEARTH_TEST_PASS"}}
	res, err := run(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Stdout)
	if strings.Contains(out, "leak") {
		t.Fatalf("environment leaked: %s", out)
	}
	if !strings.Contains(out, "HEARTH_TEST_PASS=passed") || !strings.Contains(out, "PATH=/usr/bin:/bin") {
		t.Fatalf("expected env missing: %s", out)
	}
}

func TestRunTimeout(t *testing.T) {
	spec := Spec{Paths: []string{"/bin/sleep"}, Validate: anyArgs, Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, err := run(context.Background(), spec, []string{"30"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout took %v", d)
	}
}

func TestRunOutputCapped(t *testing.T) {
	spec := Spec{Paths: []string{"/usr/bin/yes"}, Validate: anyArgs, Timeout: 2 * time.Second}
	res, _ := run(context.Background(), spec, nil)
	if !res.Truncated || len(res.Stdout) != maxOutput {
		t.Fatalf("truncated %v, len %d", res.Truncated, len(res.Stdout))
	}
}

func TestResolveRejectsUnsafeBinaries(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "true")
	copyFile(t, "/bin/true", bin)

	if err := os.Chmod(bin, 0o777); err != nil { // #nosec G302 -- testing that writable binaries are rejected
		t.Fatal(err)
	}
	if _, err := resolve([]string{bin}); !errors.Is(err, ErrBinary) {
		t.Fatalf("world-writable: got %v", err)
	}

	if err := os.Chmod(bin, 0o755); err != nil { // #nosec G302 -- executable fixture
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := resolve([]string{bin}); !errors.Is(err, ErrBinary) {
			t.Fatalf("user-owned: got %v", err)
		}
	}

	// An unsafe candidate is an error even if a later one is fine.
	if err := os.Chmod(bin, 0o777); err != nil { // #nosec G302 -- see above
		t.Fatal(err)
	}
	if _, err := resolve([]string{bin, "/bin/true"}); !errors.Is(err, ErrBinary) {
		t.Fatalf("fallthrough past unsafe candidate: got %v", err)
	}

	if _, err := resolve([]string{dir}); !errors.Is(err, ErrBinary) {
		t.Fatalf("directory: got %v", err)
	}
	if _, err := resolve([]string{filepath.Join(dir, "missing")}); !errors.Is(err, ErrBinary) {
		t.Fatalf("missing: got %v", err)
	}
}

func TestAllowlistPathsAreAbsolute(t *testing.T) {
	for name, spec := range allowlist {
		if spec.Validate == nil {
			t.Errorf("%s: no validator", name)
		}
		for _, p := range spec.Paths {
			if !filepath.IsAbs(p) || filepath.Clean(p) != p {
				t.Errorf("%s: path %q is not absolute and clean", name, p)
			}
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src) // #nosec G304 -- fixed system binary path from the test
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil { // #nosec G703 -- dst is under t.TempDir()
		t.Fatal(err)
	}
}
