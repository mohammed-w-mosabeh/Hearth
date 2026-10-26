package broker

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestDockerValidators(t *testing.T) {
	hex64 := strings.Repeat("ab", 32)
	cases := []struct {
		v    func(string) bool
		name string
		ok   []string
		bad  []string
	}{
		{ValidContainer, "container",
			[]string{"web", "my_app-1.2", "Web", hex64, hex64[:12]},
			[]string{"", "-f", ".x", "_x", "/web", "a/b", "a b", "a?b", "a%2f", "..", "a\n", strings.Repeat("a", 129)}},
		{ValidNetwork, "network", []string{"bridge", "app_default"}, []string{"", "-x", "a/b"}},
		{ValidVolume, "volume", []string{"data", hex64, "a.b_c-d", strings.Repeat("v", 255)}, []string{"", "-v", "/data", "../x", "a/b", strings.Repeat("v", 256)}},
		{ValidImage, "image",
			[]string{"nginx", "nginx:1.27-alpine", "library/nginx", "ghcr.io/me/app:v1", "localhost:5000/a/b",
				"a__b", "a-b--c", "x@sha256:" + hex64, "x:1@sha256:" + hex64, "sha256:" + hex64, hex64[:12]},
			[]string{"", "-nginx", "Nginx", "nginx:", "nginx:-1", ":1", "a/../b", "a//b", "/a", "a/", "a b", "a?b", "a#b",
				"a%2fb", "nginx@sha256:short", "https://x/y", "a..b", strings.Repeat("a", 256)}},
		{ValidImageName, "image name", []string{"x", "me/x", "localhost:5000/x"}, []string{"x:1", "x@sha256:" + hex64, "X"}},
		{ValidImageTag, "tag", []string{"latest", "1.2_3-x", "V"}, []string{"", "-1", ".1", "a/b", "a:b", strings.Repeat("t", 129)}},
	}
	for _, c := range cases {
		for _, s := range c.ok {
			if !c.v(s) {
				t.Errorf("%s %q refused", c.name, s)
			}
		}
		for _, s := range c.bad {
			if c.v(s) {
				t.Errorf("%s %q accepted", c.name, s)
			}
		}
	}
}

func TestParseImageRef(t *testing.T) {
	d := "sha256:" + strings.Repeat("0", 64)
	for in, want := range map[string][3]string{
		"nginx":               {"nginx", "", ""},
		"localhost:5000/a:v2": {"localhost:5000/a", "v2", ""},
		"a/b@" + d:            {"a/b", "", d},
		"a:1@" + d:            {"a", "1", d},
	} {
		n, tg, dg, ok := ParseImageRef(in)
		if !ok || [3]string{n, tg, dg} != want {
			t.Errorf("%s: %q %q %q %v", in, n, tg, dg, ok)
		}
	}
}

func TestMaskEnv(t *testing.T) {
	in := []string{"PATH=/usr/bin", "MYSQL_ROOT_PASSWORD=x", "github_token=y", "AWS_SECRET_ACCESS_KEY=z", "PASSWD=", "noequals", "HOME=/root"}
	want := []string{"PATH=/usr/bin", "MYSQL_ROOT_PASSWORD=" + EnvMask, "github_token=" + EnvMask, "AWS_SECRET_ACCESS_KEY=" + EnvMask, "PASSWD=" + EnvMask, "noequals", "HOME=/root"}
	if got := MaskEnv(in); !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestRisks(t *testing.T) {
	var safe HostConfig
	safe.Binds = []string{"/home/me/site:/usr/share/nginx/html:ro", "data:/data", "/etcetera:/x"}
	if r := Risks(safe); len(r) != 0 {
		t.Fatalf("safe config: %v", r)
	}
	bad := HostConfig{Privileged: true, CapAdd: []string{"SYS_ADMIN"}, PidMode: "host", NetworkMode: "host", IpcMode: "host",
		Binds: []string{"/:/host", "/etc/passwd:/p", "/var/run/docker.sock:/s", "/root/:/r", "/proc:/p2", "/sys/fs:/s2", "/dev:/d", "/run:/run", "/srv/x/../../etc:/e"}}
	bad.Mounts = append(bad.Mounts, struct{ Type, Source string }{"bind", "/home/me/.docker/run/docker.sock"}, struct{ Type, Source string }{"volume", "/etc"})
	want := []string{RiskPrivileged, RiskCapAdd, RiskHostPID, RiskHostNet, RiskHostIPC}
	for _, s := range []string{"/", "/etc/passwd", "/var/run/docker.sock", "/root/", "/proc", "/sys/fs", "/dev", "/run", "/srv/x/../../etc", "/home/me/.docker/run/docker.sock"} {
		want = append(want, RiskHostMount+":"+s)
	}
	if got := Risks(bad); !slices.Equal(got, want) {
		t.Fatalf("\n got %v\nwant %v", got, want)
	}
}

// pathSafe: an accepted value cannot change the request beyond its own
// path segment(s).
func pathSafe(t *testing.T, s string, slashOK bool) {
	t.Helper()
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") ||
		strings.ContainsAny(s, "?#%\\ \t\r\n\x00") || strings.Contains(s, "..") && slashOK {
		t.Fatalf("accepted %q", s)
	}
	if !slashOK && strings.Contains(s, "/") {
		t.Fatalf("accepted %q with /", s)
	}
	if u, err := url.Parse("http://docker/v1/x/" + s + "/json"); err != nil || u.RawQuery != "" || u.Fragment != "" {
		t.Fatalf("%q changes the URL", s)
	}
}

func FuzzValidContainer(f *testing.F) {
	for _, s := range []string{"web", "../x", "a/b", "-f", strings.Repeat("a", 64)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidContainer(s) {
			pathSafe(t, s, false)
		}
		if ValidVolume(s) {
			pathSafe(t, s, false)
		}
	})
}

func FuzzValidImage(f *testing.F) {
	for _, s := range []string{"nginx", "ghcr.io/a/b:1", "a/../b", "x@sha256:" + strings.Repeat("a", 64), "a:b:c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !ValidImage(s) {
			return
		}
		// Tags may contain ".." but follow the last ":" after the last "/",
		// so they never make a path component.
		for _, seg := range strings.Split(s, "/") {
			if seg == ".." || seg == "." || seg == "" {
				t.Fatalf("accepted %q", s)
			}
		}
		if strings.ContainsAny(s, "?#%\\ \t\r\n\x00") || strings.HasPrefix(s, "-") {
			t.Fatalf("accepted %q", s)
		}
		if n, _, _, ok := ParseImageRef(s); ok && strings.HasPrefix(n, "-") {
			t.Fatalf("name %q", n)
		}
	})
}
