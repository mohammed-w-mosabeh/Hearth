package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		msg      string
		src      string
		accepted bool
		ok       bool
	}{
		{"Accepted publickey for me from 192.0.2.1 port 50000 ssh2: ED25519 SHA256:abc", "192.0.2.1", true, true},
		{"Accepted password for me from 2001:db8::1 port 50000 ssh2", "2001:db8::1", true, true},
		{"Accepted password for me from ::ffff:192.0.2.9 port 50000 ssh2", "192.0.2.9", true, true},
		// A zone is free text to netip.ParseAddr and says nothing about the source.
		{"Accepted password for me from fe80::1%eth0 port 50000 ssh2", "fe80::1", true, true},
		{"Invalid user x from fe80::2%\"<b> port 22", "fe80::2", false, true},
		{"Failed password for me from 192.0.2.2 port 1 ssh2", "192.0.2.2", false, true},
		{"Failed publickey for me from host.example port 1 ssh2", "host.example", false, true},
		{"Invalid user admin from 192.0.2.3 port 22", "192.0.2.3", false, true},
		// One attempt, not two: "Invalid user" counted it.
		{"Failed password for invalid user admin from 192.0.2.3 port 22 ssh2", "", false, false},
		{"Failed none for me from 192.0.2.2 port 1 ssh2", "", false, false},
		// A name cannot pose as the source, nor as success.
		{"Invalid user x from 203.0.113.9 port 1 from 192.0.2.4 port 22", "192.0.2.4", false, true},
		{"Failed password for x from 203.0.113.9 port 1 ssh2 from 192.0.2.5 port 22 ssh2", "192.0.2.5", false, true},
		{"Invalid user Accepted password for me from 203.0.113.9 port 1 ssh2 from 192.0.2.6 port 22", "192.0.2.6", false, true},
		// Not sources.
		{"Invalid user x from <img/src=x> port 22", "", false, false},
		{"Invalid user x from UNKNOWN port 65535", "", false, false},
		{"Connection closed by 192.0.2.7 port 22", "", false, false},
		{"pam_unix(sshd:session): session opened for user me(uid=1000)", "", false, false},
	}
	for _, c := range cases {
		src, accepted, ok := classify(c.msg)
		if src != c.src || accepted != c.accepted || ok != c.ok {
			t.Errorf("%q: got %q %v %v", c.msg, src, accepted, ok)
		}
	}
}

func FuzzClassify(f *testing.F) {
	f.Add("Accepted publickey for me from 192.0.2.1 port 50000 ssh2: ED25519 SHA256:abc")
	f.Add("Invalid user <b> from 192.0.2.3 port 22")
	f.Fuzz(func(t *testing.T, msg string) {
		src, _, ok := classify(msg)
		if ok != (src != "") || strings.ContainsAny(src, "<>\"' \n") {
			t.Fatalf("%q gave %q", msg, src)
		}
	})
}

func entry(at time.Time, msg string) string {
	b, _ := json.Marshal(map[string]string{"MESSAGE": msg, "__REALTIME_TIMESTAMP": fmt.Sprint(at.UnixMicro())})
	return string(b) + "\n"
}

func TestParseSSH(t *testing.T) {
	loc := time.FixedZone("x", 3*3600)
	// 22:30 UTC is already the next day here.
	late := time.Date(2026, 11, 1, 22, 30, 0, 0, time.UTC)
	early := late.Add(-2 * time.Hour)
	in := entry(late, "Accepted password for me from 192.0.2.1 port 1 ssh2") +
		entry(early, "Failed password for me from 192.0.2.1 port 1 ssh2") +
		entry(early, "Invalid user a from 192.0.2.2 port 1") +
		entry(early, "Invalid user b from 192.0.2.2 port 1") +
		entry(early, "Connection closed") + "not json\n"
	sources, more, days, lines := parseSSH([]byte(in), loc)
	if lines != 6 || more != 0 {
		t.Fatalf("lines %d more %d", lines, more)
	}
	if len(sources) != 2 || sources[0].Source != "192.0.2.1" || sources[0].Accepted != 1 || sources[0].Failed != 1 ||
		sources[0].Last != late.UnixMilli() || sources[1].Failed != 2 {
		t.Fatalf("a source that got in leads: %+v", sources)
	}
	if len(days) != 2 || days[0] != (Day{"2026-11-02", 1, 0}) || days[1] != (Day{"2026-11-01", 0, 3}) {
		t.Fatalf("days: %+v", days)
	}
}

func TestParseSSHBoundsSources(t *testing.T) {
	var b strings.Builder
	at := time.Unix(1790000000, 0)
	for i := range maxSources + 5 {
		b.WriteString(entry(at, fmt.Sprintf("Invalid user a from 10.0.%d.%d port 1", i/250, i%250+1)))
	}
	sources, more, _, _ := parseSSH([]byte(b.String()), time.UTC)
	if len(sources) != maxSources || more != 5 {
		t.Fatalf("%d, %d", len(sources), more)
	}
}

func TestParseUsers(t *testing.T) {
	accounts := `root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
svc:x:900:900::/srv:/usr/sbin/nologin
me:x:1000:1000:Me Myself:/home/me:/bin/bash
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
locked:x:1001:1001::/home/locked:/usr/sbin/nologin
broken line
`
	group := `root:x:0:
sudo:x:27:me,ghost
docker:x:999:svc
users:x:100:me,ghost
me:x:1000:
locked:x:1001:
`
	got := parseUsers([]byte(accounts), []byte(group))
	names := []string{}
	for _, u := range got {
		names = append(names, u.Name)
	}
	if want := "root svc me ghost locked"; strings.Join(names, " ") != want {
		t.Fatalf("%v, want %s", names, want)
	}
	if got[0].Sudo || !got[0].Login {
		t.Errorf("root: %+v", got[0])
	}
	if !got[1].Docker || got[1].Login {
		t.Errorf("a system account in docker is shown: %+v", got[1])
	}
	if !got[2].Sudo || got[2].Docker || strings.Join(got[2].Groups, ",") != "me,sudo,users" {
		t.Errorf("me: %+v", got[2])
	}
	if got[3].UID != -1 || !got[3].Sudo || len(got[3].Groups) != 2 {
		t.Errorf("a member without an account still counts: %+v", got[3])
	}
	if got[4].Login {
		t.Errorf("locked: %+v", got[4])
	}
}

func TestParseUsersCleans(t *testing.T) {
	got := parseUsers([]byte("a\x1b[2J:x:1000:1000::/h:/bin/sh\n"), nil)
	if len(got) != 1 || got[0].Name != "a[2J" {
		t.Fatalf("%+v", got)
	}
}

func TestParseSession(t *testing.T) {
	p := map[string]dbus.Variant{"Class": dbus.MakeVariant("user"), "Type": dbus.MakeVariant("tty"), "TTY": dbus.MakeVariant("pts/0"),
		"Remote": dbus.MakeVariant(true), "RemoteHost": dbus.MakeVariant("<b>\x07host"), "Timestamp": dbus.MakeVariant(uint64(5_000_000)), "State": dbus.MakeVariant("active")}
	s, ok := parseSession("3", 1000, "me", "", p)
	if !ok || s.RemoteHost != "<b>host" || !s.Remote || s.Since != 5000 || s.TTY != "pts/0" {
		t.Fatalf("%+v", s)
	}
	if _, ok := parseSession("4", 1000, "me", "", map[string]dbus.Variant{"Class": dbus.MakeVariant("manager")}); ok {
		t.Fatal("a service manager is not a session")
	}
}

type fakeBus struct{ closed bool }

func (f *fakeBus) GetAll(_ context.Context, _ string, p dbus.ObjectPath, _ string) (map[string]dbus.Variant, error) {
	if p == "/gone" {
		return nil, errors.New("gone")
	}
	return map[string]dbus.Variant{"Class": dbus.MakeVariant("user")}, nil
}

func (f *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, _ string, out []any, _ ...any) error {
	rows, _ := out[0].(*[][]any)
	*rows = [][]any{{"1", uint32(1000), "me", "seat0", dbus.ObjectPath("/s1")}, {"2", uint32(1000), "me", "", dbus.ObjectPath("/gone")}, {"short"}}
	return nil
}
func (f *fakeBus) Close() error { f.closed = true; return nil }

func etc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"passwd": "me:x:1000:1000::/h:/bin/sh\n", "group": "sudo:x:27:me\n"} // #nosec G101 -- a fake account file, it holds no password
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRead(t *testing.T) {
	bus := &fakeBus{}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	var args []string
	a := New(Config{EtcRoot: etc(t), Now: func() time.Time { return now }, CanReadSystem: func() bool { return true },
		Bus: func(context.Context) (Bus, error) { return bus, nil },
		Run: func(_ context.Context, name string, got ...string) (safeexec.Result, error) {
			args = got
			if err := safeexec.Check(name, got...); err != nil {
				t.Fatal(err)
			}
			return safeexec.Result{Stdout: []byte(entry(now, "Invalid user a from 192.0.2.2 port 1"))}, nil
		}})
	r := a.Read(context.Background())
	if len(args) == 0 || len(r.Sources) != 1 || r.Limited || r.Truncated {
		t.Fatalf("%+v", r)
	}
	if len(r.Sessions) != 1 || !bus.closed {
		t.Fatalf("sessions %+v closed %v", r.Sessions, bus.closed)
	}
	if len(r.Users) != 1 || !r.Users[0].Sudo {
		t.Fatalf("users %+v", r.Users)
	}
}

func TestReadPartsFailAlone(t *testing.T) {
	a := New(Config{EtcRoot: filepath.Join(t.TempDir(), "none"), CanReadSystem: func() bool { return false },
		Bus: func(context.Context) (Bus, error) { return nil, errors.New("no bus") },
		Run: func(context.Context, string, ...string) (safeexec.Result, error) {
			return safeexec.Result{ExitCode: 1}, errors.New("no entries")
		}})
	r := a.Read(context.Background())
	if !r.Limited || !r.SessionsError || !r.UsersError || r.Sources == nil || r.Sessions == nil || r.Users == nil {
		t.Fatalf("%+v", r)
	}
}
