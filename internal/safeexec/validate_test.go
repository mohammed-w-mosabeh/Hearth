package safeexec

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

type vcase struct {
	in string
	ok bool
}

func runCases(t *testing.T, v Validator, cases []vcase) {
	t.Helper()
	for _, c := range cases {
		err := v(c.in)
		if c.ok && err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
		}
		if !c.ok {
			if err == nil {
				t.Errorf("%q: accepted, want rejection", c.in)
			} else if !errors.Is(err, ErrInvalidArg) {
				t.Errorf("%q: error %v does not wrap ErrInvalidArg", c.in, err)
			}
		}
	}
}

func TestDebPackage(t *testing.T) {
	runCases(t, DebPackage, []vcase{
		{"bash", true}, {"libc6", true}, {"g++", true}, {"libstdc++6", true},
		{"python3.13", true}, {"0ad", true}, {"lib-foo.bar+baz", true},
		{"", false}, {"a", false}, {"-rf", false}, {"--purge", false},
		{"Bash", false}, {"foo bar", false}, {"foo;id", false}, {"foo\n", false},
		{"+foo", false}, {".foo", false}, {"foo_bar", false}, {"foo:amd64", false},
		{"foo\x00", false},
	})
}

func TestUnitName(t *testing.T) {
	runCases(t, UnitName, []vcase{
		{"ssh.service", true}, {"getty@tty1.service", true}, {"getty@.service", true},
		{"dev-sda1.device", true}, {"home.mount", true}, {"user-1000.slice", true},
		{"systemd-fsck@dev-disk-by\\x2duuid-1234.service", true},
		{"hearth-job-backup.timer", true}, {"sys-devices-a:b.device", true},
		{"", false}, {".service", false}, {"-.mount", false}, {"--now", false},
		{"ssh", false}, {"ssh.serv", false}, {"ssh service", false},
		{"a@b@c.service", false}, {"@x.service", false}, {"ssh.service;id", false},
		{"../x.service", false}, {"x/y.service", false}, {"ssh.service\n", false},
		{strings.Repeat("a", 250) + ".service", false},
	})
}

func TestContainerID(t *testing.T) {
	runCases(t, ContainerID, []vcase{
		{"0123456789ab", true}, {strings.Repeat("f", 64), true},
		{"0123456789a", false}, {strings.Repeat("f", 65), false},
		{"0123456789AB", false}, {"nginx", false}, {"-0123456789ab", false},
		{"0123456789ab ", false},
	})
}

func TestPathUnder(t *testing.T) {
	v := PathUnder("/home/u", "/tmp")
	runCases(t, v, []vcase{
		{"/home/u", true}, {"/home/u/x", true}, {"/home/u/a b/c", true}, {"/tmp/x", true},
		{"", false}, {"home/u", false}, {"/home/u/../root", false}, {"/home/u/./x", false},
		{"/home/u/", false}, {"/home/user", false}, {"/home", false}, {"/etc/passwd", false},
		{"//home/u", false}, {"/home/u/x\n", false}, {"/tmp\x00/x", false},
	})
	runCases(t, PathUnder("/"), []vcase{{"/", true}, {"/etc", true}, {"x", false}})

	defer func() {
		if recover() == nil {
			t.Error("PathUnder accepted a relative root")
		}
	}()
	PathUnder("tmp")
}

func TestHTTPURL(t *testing.T) {
	runCases(t, HTTPURL, []vcase{
		{"http://127.0.0.1:8080/auth?t=abc", true}, {"https://example.com", true},
		{"http://localhost:3000/path#frag", true}, {"http://[::1]:9000/", true},
		{"", false}, {"example.com", false}, {"file:///etc/passwd", false},
		{"javascript:alert(1)", false}, {"ftp://x", false}, {"http://", false},
		{"http://user:pw@host/", false}, {"http:opaque", false}, {"-http://x", false},
		{"http://x/ y", false}, {"http://x/\ny", false}, {"http://x\\y", false},
		{"http://:80/", false},
	})
}

func TestLiteral(t *testing.T) {
	runCases(t, Literal("--user"), []vcase{{"--user", true}, {"--user ", false}, {"--system", false}, {"", false}})
}

func TestSystemdRunShape(t *testing.T) {
	v := allowlist["systemd-run-xdg-open"].Validate
	ok := []string{"--user", "--collect", "--quiet", "--", "/usr/bin/xdg-open", "http://127.0.0.1:7380/auth?t=ab"}
	if err := v(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{
		{"--user", "--collect", "--quiet", "--", "/bin/sh", "http://x"},
		{"--user", "--collect", "--quiet", "--", "/usr/bin/xdg-open", "file:///etc/passwd"},
		{"--system", "--collect", "--quiet", "--", "/usr/bin/xdg-open", "http://x"},
		{"--user", "--collect", "--quiet", "--", "/usr/bin/xdg-open", "http://x", "extra"},
		{"--user", "-p", "User=root", "--", "/usr/bin/xdg-open", "http://x"},
	} {
		if v(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestNvidiaSMIShape(t *testing.T) {
	v := allowlist["nvidia-smi"].Validate
	ok := []string{"--query-gpu=pci.bus_id,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw", "--format=csv,noheader,nounits"}
	if err := v(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{
		{"-r"}, // GPU reset
		{"--query-gpu=name", "--format=csv"},
		{ok[0], ok[1], "-i", "0"},
		{"--gom=COMPUTE", ok[1]},
	} {
		if v(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestExactly(t *testing.T) {
	v := exactly(DebPackage, UnitName)
	if err := v([]string{"bash", "ssh.service"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"bash"}, {"bash", "ssh.service", "x"}, {"ssh.service", "bash"}} {
		if v(args) == nil {
			t.Errorf("%q accepted", args)
		}
	}
}

// Fuzz invariants: whatever a validator accepts must be free of control
// characters and must not look like an option.

func checkAccepted(t *testing.T, s string) {
	t.Helper()
	if strings.HasPrefix(s, "-") {
		t.Fatalf("accepted option-like %q", s)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			t.Fatalf("accepted control char in %q", s)
		}
	}
}

func FuzzDebPackage(f *testing.F) {
	for _, s := range []string{"bash", "g++", "-rf", "foo bar", "libc6\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if DebPackage(s) != nil {
			return
		}
		checkAccepted(t, s)
		if !debPackageRe.MatchString(s) || strings.ContainsAny(s, " /;") {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzUnitName(f *testing.F) {
	for _, s := range []string{"ssh.service", "getty@tty1.service", "-.mount", "a@b@c.service", "x/y.service"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if UnitName(s) != nil {
			return
		}
		checkAccepted(t, s)
		if len(s) > 255 || strings.ContainsAny(s, "/ ") || strings.Count(s, "@") > 1 {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzContainerID(f *testing.F) {
	for _, s := range []string{"0123456789ab", strings.Repeat("a", 64), "nginx"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ContainerID(s) != nil {
			return
		}
		checkAccepted(t, s)
		if len(s) < 12 || len(s) > 64 || strings.Trim(s, "0123456789abcdef") != "" {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzPathUnder(f *testing.F) {
	roots := []string{"/home/u", "/tmp"}
	v := PathUnder(roots...)
	for _, s := range []string{"/home/u/x", "/home/u/../etc", "/home/user", "relative", "/tmp//x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if v(s) != nil {
			return
		}
		checkAccepted(t, s)
		if filepath.Clean(s) != s || !filepath.IsAbs(s) {
			t.Fatalf("accepted unclean %q", s)
		}
		inside := false
		for _, r := range roots {
			if rel, err := filepath.Rel(r, s); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				inside = true
			}
		}
		if !inside {
			t.Fatalf("accepted %q outside roots", s)
		}
	})
}

func FuzzHTTPURL(f *testing.F) {
	for _, s := range []string{"http://127.0.0.1:1/auth?t=x", "javascript:alert(1)", "http://u:p@h/", "-x", "file:///"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if HTTPURL(s) != nil {
			return
		}
		checkAccepted(t, s)
		lower := strings.ToLower(s)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			t.Fatalf("accepted non-http %q", s)
		}
		if strings.ContainsAny(s, " \\") {
			t.Fatalf("accepted %q", s)
		}
	})
}

func TestJournalShape(t *testing.T) {
	ok := func(extra ...string) []string { return append(append([]string{}, JournalFixedArgs...), extra...) }
	good := [][]string{
		ok("--lines=200", "--unit=cups.service"),
		ok("--lines=1", "--user-unit=syncnotes.service"),
		ok("--lines=1000", "--unit=getty@tty1.service", "--after-cursor=s=8a1b;i=2f;b=3c;m=4d;t=5e;x=6f"),
	}
	for _, a := range good {
		if err := journalArgs(a); err != nil {
			t.Errorf("%q: %v", a, err)
		}
	}
	bad := [][]string{
		ok("--lines=0", "--unit=cups.service"),
		ok("--lines=1001", "--unit=cups.service"),
		ok("--lines=020", "--unit=cups.service"),
		ok("--lines=10", "--unit=-x.service"),
		ok("--lines=10", "--unit=cups"),
		ok("--lines=10", "--identifier=cups.service"),
		ok("--lines=10", "--unit=cups.service", "--after-cursor=s=1;--flush"),
		ok("--lines=10", "--unit=cups.service", "--since=yesterday"),
		ok("--lines=10", "--unit=cups.service", "--after-cursor=s=1", "--merge"),
		ok("--lines=10"),
		append([]string{"--output=export"}, ok("--lines=10", "--unit=cups.service")[1:]...),
		{"--lines=10", "--unit=cups.service"},
	}
	for _, a := range bad {
		if journalArgs(a) == nil {
			t.Errorf("accepted %q", a)
		}
	}
}

func FuzzJournalArgs(f *testing.F) {
	f.Add("200", "cups.service", "s=8a1b;i=2f")
	f.Fuzz(func(t *testing.T, lines, unit, cursor string) {
		args := append(append([]string{}, JournalFixedArgs...), "--lines="+lines, "--unit="+unit, "--after-cursor="+cursor)
		if journalArgs(args) != nil {
			return
		}
		for _, a := range args[len(JournalFixedArgs):] {
			_, v, _ := strings.Cut(a, "=")
			if strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t\n/") {
				t.Fatalf("accepted %q", a)
			}
		}
	})
}

func TestImageRef(t *testing.T) {
	runCases(t, ImageRef, []vcase{
		{"nginx", true}, {"nginx:1.27-alpine", true}, {"ghcr.io/photos-app/photos-server:v3", true},
		{"localhost:5000/app:dev", true}, {"sha256:" + strings.Repeat("a", 64), true},
		{"", false}, {"-q", false}, {"--input=/etc/shadow", false}, {"Nginx", false}, {"nginx:", false},
		{"a/../b", false}, {"nginx latest", false}, {"nginx\n", false}, {"nginx@sha256:abc", false},
		{"/abs", false}, {"a:b:c", false},
	})
}

func TestTrivyCacheDir(t *testing.T) {
	runCases(t, TrivyCacheDir, []vcase{
		{"--cache-dir=/home/me/.local/share/hearth/trivy", true},
		{"--cache-dir=relative/hearth/trivy", false}, {"--cache-dir=/home/me/../x/hearth/trivy", false},
		{"--cache-dir=/tmp", false}, {"/home/me/.local/share/hearth/trivy", false}, {"--cache-dir=", false},
	})
}

func TestTrivyArgs(t *testing.T) {
	ok := []string{"image", "--quiet", "--scanners=vuln", "--timeout=9m", "--format=template",
		"--template=" + TrivyTemplate, "--cache-dir=/h/.local/share/hearth/trivy", "nginx:latest"}
	if err := allowlist["trivy-image"].Validate(ok); err != nil {
		t.Fatal(err)
	}
	for i, bad := range [][]string{
		append(append([]string{}, ok[:7]...), "--server=http://x"),
		append([]string{"image", "--quiet", "--scanners=vuln,secret"}, ok[3:]...),
		append(append([]string{}, ok...), "extra"),
		append(append([]string{}, ok[:5]...), "--template=@/etc/passwd", ok[6], ok[7]),
	} {
		if allowlist["trivy-image"].Validate(bad) == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func FuzzImageRef(f *testing.F) {
	for _, s := range []string{"nginx:1", "ghcr.io/a/b:v", "-q", "a/../b", "x\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ImageRef(s) != nil {
			return
		}
		checkAccepted(t, s)
		if strings.ContainsAny(s, " =@\\") || strings.Contains(s, "..") {
			t.Fatalf("accepted %q", s)
		}
	})
}
