package spec

import (
	"strings"
	"testing"
)

func TestBinds(t *testing.T) {
	s := Spec{Services: map[string]Service{
		"web": {Volumes: []Mount{{Type: "bind", Source: "/srv/b", Target: "/z"}, {Type: "volume", Source: "v", Target: "/v"}, {Type: "bind", Source: "/srv/a", Target: "/a"}}},
		"db":  {Volumes: []Mount{{Type: "bind", Source: "/srv/c", Target: "/c"}}},
	}}
	got := Binds(s)
	want := []Bind{{"db", "/srv/c", "/c"}, {"web", "/srv/a", "/a"}, {"web", "/srv/b", "/z"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestCheckResolved(t *testing.T) {
	s := Spec{BindRoots: []string{"/srv/app", "/home/u/media"}}
	roots := map[string]string{"/srv/app": "/srv/app", "/home/u/media": "/mnt/disk/media"}
	cases := []struct {
		src, real string
		ok        bool
		why       string
	}{
		{"/srv/app/data", "/srv/app/data", true, ""},
		{"/srv/app", "/srv/app", true, ""},
		{"/home/u/media/films", "/mnt/disk/media/films", true, ""},
		{"/srv/app/data", "/srv/other", false, "outside its approved root"},
		{"/srv/app/data", "/mnt/disk/media/x", false, "outside"}, // the other root's real path, reached from this one
		{"/srv/app/conf", "/etc/shadow", false, "never allowed"},
		{"/srv/app/conf", "/etc", false, "never allowed"},
		{"/srv/app/st", "/var/lib/hearth/stacks", false, "never allowed"},
		{"/srv/app/st", "/var/lib/docker/volumes", false, "never allowed"},
		{"/srv/app/b", "/boot/efi", false, "never allowed"},
		{"/srv/app/r", "/", false, "never allowed"},
		{"/srv/app/s", "/run/docker.sock", false, "its own opt-in"},
		{"/srv/app/s", "/root/.ssh", false, "its own opt-in"},
		{"/srv/app/s", "/sys/kernel", false, "its own opt-in"},
		{"/srv/app/d", "/dev/sda", false, "raw disk"},
		{"/srv/app/x", "/srv/app/x (deleted)", true, ""}, // clean and absolute; the open itself fails first in practice
		{"/srv/app/x", "relative", false, "not absolute"},
		{"/srv/app/x", "/srv/app/../etc", false, "not absolute"},
		// Flagged sources: no link at all, except /var/run.
		{"/run/docker.sock", "/run/docker.sock", true, ""},
		{"/var/run/docker.sock", "/run/docker.sock", true, ""},
		{"/run/docker.sock", "/run/user/1000/docker.sock", false, "through a link"},
		{"/root/data", "/root/data", true, ""},
		{"/root/data", "/home/u/data", false, "through a link"},
		{"/sys/x", "/etc/x", false, "never allowed"},
	}
	for _, c := range cases {
		why := CheckResolved(s, roots, c.src, c.real)
		if (why == "") != c.ok || (c.why != "" && !strings.Contains(why, c.why)) {
			t.Errorf("%s -> %s: %q", c.src, c.real, why)
		}
	}
	// A root whose own resolution is missing or refused approves nothing.
	if why := CheckResolved(s, map[string]string{"/home/u/media": "/etc"}, "/home/u/media/x", "/home/u/media/x"); why == "" {
		t.Error("a root resolved to /etc approved a bind")
	}
	if why := CheckResolved(s, nil, "/srv/app/x", "/srv/app/x"); why == "" {
		t.Error("an unresolved root approved a bind")
	}
}

func TestCheckRoot(t *testing.T) {
	for p, ok := range map[string]bool{
		"/srv/app": true, "/mnt/disk": true, "/home/u": true,
		"/": false, "/etc": false, "/etc/x": false, "/var/lib/hearth": false, "/root": false,
		"/run/user/1000": false, "/proc/1": false, "rel": false, "/a/../b": false,
	} {
		if (CheckRoot(p) == "") != ok {
			t.Errorf("CheckRoot(%q) = %q", p, CheckRoot(p))
		}
	}
}
