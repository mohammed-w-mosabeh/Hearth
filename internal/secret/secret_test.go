package secret

import (
	"slices"
	"strings"
	"testing"
)

func TestName(t *testing.T) {
	for _, k := range []string{"MYSQL_ROOT_PASSWORD", "PASSWD", "db_pwd", "github_token", "AWS_SECRET_ACCESS_KEY",
		"BASIC_AUTH", "GOOGLE_APPLICATION_CREDENTIALS", "DATABASE_URL", "mongo_uri", "SENTRY_DSN"} {
		if !Name(k) {
			t.Errorf("%s not masked", k)
		}
	}
	for _, k := range []string{"PATH", "HOME", "TZ", "URL", "URL_PREFIX", "PUID", "NODE_ENV", "BASE_URLS"} {
		if Name(k) {
			t.Errorf("%s masked", k)
		}
	}
}

func TestValue(t *testing.T) {
	for _, v := range []string{"postgres://app:hunter2@db:5432/app", "redis://:pw@cache:6379", "https://user@example.com/",
		"amqp://guest:guest@rabbit", "  //u:p@host/x", "HTTPS://U:P@H"} {
		if !Value(v) {
			t.Errorf("%q not masked", v)
		}
	}
	for _, v := range []string{"", "https://example.com/a@b", "http://host:8080/path", "me@example.com", "git@github.com:a/b.git",
		"mailto:me@example.com", "/data", "Europe/Berlin", "https://example.com/?u=a@b", "https://example.com/#x@y"} {
		if Value(v) {
			t.Errorf("%q masked", v)
		}
	}
}

func TestEnv(t *testing.T) {
	in := []string{"PATH=/usr/bin", "MYSQL_ROOT_PASSWORD=x", "github_token=y", "AWS_SECRET_ACCESS_KEY=z", "PASSWD=", "noequals",
		"HOME=/root", "DATABASE_URL=sqlite:///data/db", "UPSTREAM=http://bob:pw@proxy:3128", "SITE=https://example.com", "A=b=c"}
	want := []string{"PATH=/usr/bin", "MYSQL_ROOT_PASSWORD=" + Mask, "github_token=" + Mask, "AWS_SECRET_ACCESS_KEY=" + Mask,
		"PASSWD=" + Mask, "noequals", "HOME=/root", "DATABASE_URL=" + Mask, "UPSTREAM=" + Mask, "SITE=https://example.com", "A=b=c"}
	if got := Env(in); !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if Env(nil) == nil || len(Env(nil)) != 0 {
		t.Fatal("nil env")
	}
}

func TestLine(t *testing.T) {
	for _, l := range []string{"export API_KEY=abc", "password = 1", `DATABASE_URL="postgres://u@h"`, "SENTRY_DSN: https://x",
		"proxy=http://bob:pw@proxy:3128", "  db_uri =x"} {
		if !Line(l) {
			t.Errorf("%q shown", l)
		}
	}
	pub := "ssh-ed25519 " + strings.Repeat("AAAAC3NzaC1lZDI1NTE5", 4) + " me@laptop"
	for _, l := range []string{"alias ll='ls -l'", "export PATH=$HOME/bin:$PATH", pub, "# see https://example.com/docs", "URL=https://example.com"} {
		if Line(l) {
			t.Errorf("%q hidden", l)
		}
	}
}

// FuzzEnv: a masked entry keeps its name and shows nothing of its value; an
// entry that names a secret or holds user info is never shown.
func FuzzEnv(f *testing.F) {
	for _, s := range []string{"DB_URL=x", "A=postgres://u:p@h", "PATH=/bin", "noequals", "=", "K=//u@h"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, e string) {
		got := Env([]string{e})[0]
		k, v, found := strings.Cut(e, "=")
		if !found {
			if got != e {
				t.Fatalf("changed %q to %q", e, got)
			}
			return
		}
		if Name(k) || Value(v) {
			if got != k+"="+Mask {
				t.Fatalf("%q shown as %q", e, got)
			}
		} else if got != e {
			t.Fatalf("changed %q to %q", e, got)
		}
		if _, gv, _ := strings.Cut(got, "="); userinfo.MatchString(gv) {
			t.Fatalf("user info shown: %q", got)
		}
	})
}
