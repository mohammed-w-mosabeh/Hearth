package smart

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// fake answers disk.smart per disk.
type fake struct {
	mu      sync.Mutex
	reports map[string]broker.SmartReport
	err     error
	calls   int
	tests   []broker.SelfTestParams
}

func (f *fake) Call(_ context.Context, method string, params, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	if method == "disk.selftest" {
		f.tests = append(f.tests, params.(broker.SelfTestParams))
		return nil
	}
	*out.(*broker.SmartReport) = f.reports[params.(broker.SmartParams).Device]
	return nil
}

func sys(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, "block", n, "device"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Virtual devices have no device link.
	if err := os.MkdirAll(filepath.Join(root, "block", "sdz"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

type seen struct {
	rounds []map[string]float64
}

func (s *seen) observe(_ context.Context, _ time.Time, v map[string]float64) {
	s.rounds = append(s.rounds, v)
}

func TestNames(t *testing.T) {
	m := New(Config{Broker: &fake{}, SysRoot: sys(t, "sdb", "nvme0n1", "sda", "loop0", "sr0", "dm-0")})
	if got := strings.Join(m.Names(), " "); got != "nvme0n1 sda sdb" {
		t.Fatal(got)
	}
}

func TestObserveAndSleep(t *testing.T) {
	f := &fake{reports: map[string]broker.SmartReport{
		"sda": {Device: "sda", Supported: true, Failing: true},
		"sdb": {Device: "sdb", Supported: true},
		"sdc": {Device: "sdc", Supported: false},
	}}
	s := &seen{}
	m := New(Config{Broker: f, SysRoot: sys(t, "sda", "sdb", "sdc"), Observe: s.observe})
	ctx := context.Background()
	m.Refresh(ctx, true)
	if len(s.rounds) != 1 || len(s.rounds[0]) != 2 || s.rounds[0]["sda"] != 1 || s.rounds[0]["sdb"] != 0 {
		t.Fatalf("%v", s.rounds)
	}
	// Asleep: the last verdict stands, so the incident stays open.
	f.reports["sda"] = broker.SmartReport{Device: "sda", Asleep: true}
	m.Refresh(ctx, true)
	if len(s.rounds) != 2 || s.rounds[1]["sda"] != 1 {
		t.Fatalf("%v", s.rounds)
	}
	d := m.Snapshot().Disks[0]
	if !d.Asleep || !d.Failing || d.Device != "sda" {
		t.Fatalf("%+v", d)
	}
	// Awake again and fine.
	f.reports["sda"] = broker.SmartReport{Device: "sda", Supported: true}
	m.Refresh(ctx, true)
	if s.rounds[2]["sda"] != 0 || m.Snapshot().Disks[0].Asleep {
		t.Fatalf("%v", s.rounds)
	}
}

// After a restart an open incident must survive a disk that sleeps or
// does not answer: hearthd knows nothing about it yet.
func TestNoVerdictNoObservation(t *testing.T) {
	f := &fake{reports: map[string]broker.SmartReport{"sda": {Device: "sda", Asleep: true}, "sdb": {Device: "sdb", Supported: true}}}
	s := &seen{}
	m := New(Config{Broker: f, SysRoot: sys(t, "sda", "sdb"), Observe: s.observe})
	m.Refresh(context.Background(), true)
	if len(s.rounds) != 0 {
		t.Fatalf("%v", s.rounds)
	}
}

func TestBrokerAway(t *testing.T) {
	for want, err := range map[string]error{
		StatusUnavailable: broker.ErrUnavailable,
		StatusNotMember:   broker.ErrNotMember,
		StatusNoSmartctl:  broker.ErrNoSmartctl,
	} {
		f := &fake{reports: map[string]broker.SmartReport{"sda": {Device: "sda", Supported: true, Failing: true}}}
		s := &seen{}
		m := New(Config{Broker: f, SysRoot: sys(t, "sda"), Observe: s.observe})
		m.Refresh(context.Background(), true)
		f.err = err
		m.Refresh(context.Background(), true)
		h := m.Snapshot()
		if h.Status != want || len(s.rounds) != 1 || len(h.Disks) != 1 || !h.Disks[0].Failing {
			t.Fatalf("%s: %+v %v", want, h, s.rounds)
		}
	}
}

func TestOneDiskError(t *testing.T) {
	f := &fake{reports: map[string]broker.SmartReport{"sda": {Device: "sda", Supported: true}}}
	m := New(Config{Broker: f, SysRoot: sys(t, "sda")})
	m.Refresh(context.Background(), true)
	f.err = broker.Errorf(broker.CodeFailed, "smartctl: boom")
	m.Refresh(context.Background(), true)
	h := m.Snapshot()
	if h.Status != StatusOK || !strings.Contains(h.Disks[0].Error, "boom") || !h.Disks[0].Supported {
		t.Fatalf("%+v", h)
	}
}

func TestHandleServesCacheWhileFresh(t *testing.T) {
	f := &fake{reports: map[string]broker.SmartReport{"sda": {Device: "sda", Supported: true}}}
	m := New(Config{Broker: f, SysRoot: sys(t, "sda")})
	for range 3 {
		w := httptest.NewRecorder()
		m.Handle(w, httptest.NewRequest(http.MethodGet, "/api/v1/disks/health", nil))
		var h Health
		if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil || h.Status != StatusOK || len(h.Disks) != 1 {
			t.Fatalf("%s %v", w.Body, err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("%d calls for one disk", f.calls)
	}
}

type noAudit struct{}

func (noAudit) Audit(context.Context, store.AuditEntry) error { return nil }

func selftest(t *testing.T, f *fake, target string, params map[string]string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	reg := actions.NewRegistry(noAudit{})
	New(Config{Broker: f, SysRoot: sys(t, "sda")}).Register(reg)
	b, _ := json.Marshal(map[string]any{"target": target, "params": params})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/actions/disk/selftest", strings.NewReader(string(b)))
	r.SetPathValue("kind", "disk")
	r.SetPathValue("action", "selftest")
	w := httptest.NewRecorder()
	reg.Handle(w, r)
	return w, w.Body.String()
}

func TestSelfTestAction(t *testing.T) {
	f := &fake{reports: map[string]broker.SmartReport{"sda": {Device: "sda", Supported: true}}}
	if w, body := selftest(t, f, "sda", map[string]string{"kind": "short"}); w.Code != 200 || !strings.Contains(body, `"done":1`) {
		t.Fatal(w.Code, body)
	}
	if len(f.tests) != 1 || f.tests[0] != (broker.SelfTestParams{Device: "sda", Kind: "short"}) {
		t.Fatalf("%+v", f.tests)
	}
	for _, c := range []struct {
		target string
		params map[string]string
	}{
		{"sda1", map[string]string{"kind": "short"}},
		{"../sda", map[string]string{"kind": "short"}},
		{"sda", map[string]string{"kind": "offline"}},
		{"sda", map[string]string{}},
		{"sda", map[string]string{"kind": "short", "x": "y"}},
	} {
		if w, body := selftest(t, f, c.target, c.params); w.Code != http.StatusBadRequest {
			t.Errorf("%+v: %d %s", c, w.Code, body)
		}
	}
	f.err = broker.ErrUnauthorized
	if _, body := selftest(t, f, "sda", map[string]string{"kind": "long"}); !strings.Contains(body, `"reason":"unauthorized"`) {
		t.Fatal(body)
	}
	f.err = errors.New("something else")
	if w, _ := selftest(t, f, "sda", map[string]string{"kind": "long"}); w.Code != http.StatusBadGateway {
		t.Fatal(w.Code)
	}
}
