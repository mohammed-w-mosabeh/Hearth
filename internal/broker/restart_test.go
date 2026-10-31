package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const nrOutput = `NEEDRESTART-VER: 3.11
NEEDRESTART-KCUR: 6.12.1
NEEDRESTART-KSTA: 1
NEEDRESTART-SVC: nginx.service
NEEDRESTART-SVC: ../evil
NEEDRESTART-SVC: --all
NEEDRESTART-SESS: me @ user manager service
NEEDRESTART-SESS: <b>x</b>` + "\x1b[31m\n"

func TestParseNeedrestart(t *testing.T) {
	r, err := parseNeedrestart(strings.NewReader(nrOutput))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Installed || len(r.Services) != 1 || r.Services[0] != "nginx.service" || len(r.Sessions) != 2 || strings.ContainsRune(r.Sessions[1], 27) {
		t.Fatalf("%+v", r)
	}
}

// No version line is a run that failed. It must never read as "nothing
// needs a restart".
func TestParseNeedrestartEmptyIsNotClean(t *testing.T) {
	for _, s := range []string{"", "Scanning processes...\n", "NEEDRESTART-SVC: a.service\n"} {
		if _, err := parseNeedrestart(strings.NewReader(s)); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestParseNeedrestartBounded(t *testing.T) {
	r, err := parseNeedrestart(strings.NewReader("NEEDRESTART-VER: 3\n" + strings.Repeat("NEEDRESTART-SVC: a.service\n", 1000)))
	if err != nil || len(r.Services) != maxRestartItems {
		t.Fatal(len(r.Services), err)
	}
}

// fakeUnit plays systemd: inactive and stale until polled twice.
type fakeUnit struct {
	output  string
	body    string
	state   string
	polls   int
	started int
	noUnit  bool
	held    bool
}

func (f *fakeUnit) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	if strings.HasSuffix(method, ".LoadUnit") {
		if f.noUnit {
			return errors.New("no unit")
		}
		*out[0].(*dbus.ObjectPath) = "/unit"
		return nil
	}
	if strings.HasSuffix(method, ".Ref") {
		f.held = true
		return nil
	}
	if !f.held {
		return errors.New("started without holding the unit")
	}
	if args[0] != NeedrestartUnit || args[1] != "replace" {
		return errors.New("wrong unit")
	}
	f.started++
	return nil
}

func (f *fakeUnit) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	f.polls++
	stamp, state := uint64(1), "inactive" // the previous run's end
	if f.polls >= 2 {
		stamp, state = uint64(time.Now().UnixMicro()), f.state
		_ = os.WriteFile(f.output, []byte(f.body), 0o600)
	}
	return map[string]dbus.Variant{"ActiveState": dbus.MakeVariant(state), "StateChangeTimestamp": dbus.MakeVariant(stamp)}, nil
}

func (f *fakeUnit) Close() error { return nil }

func nrSetup(t *testing.T, f *fakeUnit) (Method, NeedrestartConfig) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "needrestart")
	if err := os.WriteFile(bin, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.output = filepath.Join(dir, "out")
	c := NeedrestartConfig{Output: f.output, Binary: bin, Poll: time.Millisecond,
		Bus: func(context.Context) (SystemBus, error) { return f, nil }}
	return NeedrestartMethods(c)[0], c
}

func TestNeedrestartMethod(t *testing.T) {
	f := &fakeUnit{body: nrOutput, state: "inactive"}
	m, _ := nrSetup(t, f)
	if m.Privileged {
		t.Fatal("a read would raise a password dialog")
	}
	got, err := m.Handle(context.Background(), Caller{}, nil)
	if err != nil || len(got.(NeedrestartResult).Services) != 1 {
		t.Fatal(got, err)
	}
	if f.polls < 2 {
		t.Fatal("read before the run ended")
	}
	// Fresh: no second run, whoever asks.
	if _, err := m.Handle(context.Background(), Caller{}, nil); err != nil || f.started != 1 {
		t.Fatal(f.started, err)
	}
	if _, err := m.Handle(context.Background(), Caller{}, json.RawMessage(`{"args":"-r a"}`)); err == nil {
		t.Fatal("params accepted")
	}
}

func TestNeedrestartStaleRun(t *testing.T) {
	f := &fakeUnit{body: nrOutput, state: "failed"}
	m, _ := nrSetup(t, f)
	if _, err := m.Handle(context.Background(), Caller{}, nil); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatal(err)
	}
	f = &fakeUnit{noUnit: true}
	m, _ = nrSetup(t, f)
	if _, err := m.Handle(context.Background(), Caller{}, nil); err == nil {
		t.Fatal("no unit")
	}
}

func TestNeedrestartNotInstalled(t *testing.T) {
	f := &fakeUnit{}
	_, c := nrSetup(t, f)
	c.Binary = filepath.Join(t.TempDir(), "gone")
	got, err := NeedrestartMethods(c)[0].Handle(context.Background(), Caller{}, nil)
	if err != nil || got.(NeedrestartResult).Installed || f.started != 0 {
		t.Fatal(got, err)
	}
}
