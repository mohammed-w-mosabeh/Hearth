package connectivity

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hearth", "hearth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestValidate(t *testing.T) {
	for _, c := range []Config{
		{},
		{Enabled: true, Targets: []Target{{Host: "example.com", Port: 443}, {Host: "9.9.9.9"}, {Host: "2001:db8::1", Port: 53}}, DNSName: "debian.org"},
		{CapGiB: 50, CapIface: "wlp3s0"},
	} {
		if err := c.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	many := make([]Target, maxTargets+1)
	for i := range many {
		many[i] = Target{Host: "a.example", Port: i + 1}
	}
	for _, c := range []Config{
		{Enabled: true},
		{Targets: many},
		{Targets: []Target{{Host: "https://example.com"}}}, {Targets: []Target{{Host: "a b"}}}, {Targets: []Target{{Host: ""}}},
		{Targets: []Target{{Host: "-x"}}}, {Targets: []Target{{Host: "a.example\n"}}}, {Targets: []Target{{Host: "fe80::1%eth0"}}},
		{Targets: []Target{{Host: "a.example", Port: 70000}}}, {Targets: []Target{{Host: "a.example", Port: -1}}},
		{Targets: []Target{{Host: "a.example"}, {Host: "a.example"}}},
		{DNSName: "1.1.1.1"}, {DNSName: "a_b"},
		{CapGiB: -1, CapIface: "wlp3s0"}, {CapGiB: math.NaN(), CapIface: "wlp3s0"}, {CapGiB: 5}, {CapIface: "wlp3s0"}, {CapGiB: 5, CapIface: "a/b"},
	} {
		if c.Validate() == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestJudge(t *testing.T) {
	gw := func(ok bool) Result { return Result{Kind: KindGateway, OK: ok} }
	dns := func(ok bool) Result { return Result{Kind: KindDNS, OK: ok} }
	tg := func(ok bool) Result { return Result{Kind: KindTarget, OK: ok} }
	for _, c := range []struct {
		rs    []Result
		up    bool
		known bool
		cause string
	}{
		{[]Result{gw(true), dns(true), tg(true)}, true, true, ""},
		{[]Result{gw(false), dns(false), tg(true), tg(false)}, true, true, ""},
		{[]Result{gw(true), dns(true), tg(false)}, false, true, CauseUp},
		{[]Result{gw(true), dns(false), tg(false)}, false, true, CauseDNS},
		{[]Result{dns(false), gw(false), tg(false)}, false, true, CauseLocal},
		{[]Result{gw(true), {Kind: KindTarget, Skipped: true}}, false, false, CauseUp},
	} {
		up, known, cause := judge(c.rs)
		if up != c.up || known != c.known || (!up && cause != c.cause) {
			t.Errorf("%+v: %v %v %q", c.rs, up, known, cause)
		}
	}
}

// A monitor with one TCP target, answering as tests say.
func harness(t *testing.T, db *store.DB, at *time.Time, ok *bool) (*Monitor, *[]float64) {
	t.Helper()
	var seen []float64
	m := New(Options{Store: db, Now: func() time.Time { return *at }, Gateways: func() []string { return []string{"192.0.2.1"} },
		Probes: Probes{
			TCP: func(context.Context, string, int) (time.Duration, error) {
				if *ok {
					return 12 * time.Millisecond, nil
				}
				return 0, errors.New("refused")
			},
			DNS:  func(context.Context, string) (time.Duration, error) { return time.Millisecond, nil },
			Ping: func(context.Context, string) (time.Duration, error) { return time.Millisecond, nil },
		},
		Observe: func(_ context.Context, _ time.Time, metric string, v map[string]float64) {
			if metric == MetricDown {
				seen = append(seen, v[""])
			}
		}})
	m.cfg = Config{Enabled: true, DNSName: "a.example", Targets: []Target{{Host: "t.example", Port: 443}}}
	return m, &seen
}

func TestOutage(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ok := true
	m, seen := harness(t, db, &at, &ok)
	step := func() { at = at.Add(every); m.tick(ctx) }
	step()
	if pts := m.Points(); len(pts) != 3 {
		t.Fatalf("%+v", pts)
	}
	ok = false
	step() // one lost check is not an outage
	if o, _ := db.Outages(ctx, 5); len(o) != 0 {
		t.Fatalf("%+v", o)
	}
	step()
	o, err := db.Outages(ctx, 5)
	if err != nil || len(o) != 1 || o[0].Cause != CauseUp || o[0].EndedAt != nil {
		t.Fatalf("%+v %v", o, err)
	}
	ok = true
	step()
	o, _ = db.Outages(ctx, 5)
	if len(o) != 1 || o[0].EndedAt == nil {
		t.Fatalf("%+v", o)
	}
	want := []float64{0, 0, 1, 0}
	for i := range want {
		if (*seen)[i] != want[i] {
			t.Fatalf("%v, want %v", *seen, want)
		}
	}
}

func TestSleepIsNoOutage(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ok := false
	m, _ := harness(t, db, &at, &ok)
	m.tick(ctx) // one failure, then the lid closes
	at = at.Add(8 * time.Hour)
	m.tick(ctx) // on waking the network is not back yet
	if o, _ := db.Outages(ctx, 5); len(o) != 0 {
		t.Fatalf("%+v", o)
	}
}

func TestCount(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	ok := true
	m, _ := harness(t, db, &at, &ok)
	m.Count(at, "wlp3s0", 1000, 100) // a baseline: counters began before Hearth
	m.Count(at, "wlp3s0", 1500, 150)
	m.Count(at, "wlp3s0", 10, 10) // reset by a reboot
	m.Count(at, "wlp3s0", 110, 30)
	m.Flush(ctx)
	m.Count(at, "wlp3s0", 210, 40)
	m.Flush(ctx)
	rows, err := db.NetUsageSince(ctx, "2026-10-01")
	if err != nil || len(rows) != 1 || rows[0].Rx != 700 || rows[0].Tx != 80 {
		t.Fatalf("%+v %v", rows, err)
	}
	rep, err := m.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Usage) != 1 || rep.Usage[0].MonthRx != 700 || rep.Usage[0].TodayTx != 80 {
		t.Fatalf("%+v", rep.Usage)
	}
}

func TestCap(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	var got []map[string]float64
	m := New(Options{Store: db, Now: func() time.Time { return at }, Gateways: func() []string { return nil },
		Observe: func(_ context.Context, _ time.Time, metric string, v map[string]float64) {
			if metric == MetricCap {
				got = append(got, v)
			}
		}})
	if err := db.AddNetUsage(ctx, []store.NetUsage{{Iface: "wlp3s0", Day: "2026-10-02", Rx: 1 << 30, Tx: 1 << 30}, {Iface: "wlp3s0", Day: "2026-09-30", Rx: 9 << 30}, {Iface: "eth0", Day: "2026-10-02", Rx: 9 << 30}}); err != nil {
		t.Fatal(err)
	}
	m.checkCap(ctx, at, Config{CapGiB: 2, CapIface: "wlp3s0"})
	m.checkCap(ctx, at, Config{CapGiB: 3, CapIface: "wlp3s0"})
	m.checkCap(ctx, at, Config{})
	if len(got) != 3 || got[0]["wlp3s0"] != 1 || got[1]["wlp3s0"] != 0 || len(got[2]) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestSaveOffByDefault(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	probes := 0
	count := func(context.Context, string, int) (time.Duration, error) { probes++; return 0, nil }
	m := New(Options{Store: db, Now: func() time.Time { return at }, Gateways: func() []string { return []string{"192.0.2.1"} },
		Probes: Probes{TCP: count,
			DNS:  func(context.Context, string) (time.Duration, error) { probes++; return 0, nil },
			Ping: func(context.Context, string) (time.Duration, error) { probes++; return 0, nil }}})
	m.Load(ctx)
	m.tick(ctx)
	if probes != 0 {
		t.Fatalf("%d checks before the user turned them on", probes)
	}
	put := func(body string) int {
		w := httptest.NewRecorder()
		m.handleSave(w, httptest.NewRequest("PUT", "/api/v1/connectivity/config", strings.NewReader(body)))
		return w.Code
	}
	for _, bad := range []string{`{"enabled":true}`, `{"targets":[{"host":"http://x"}]}`, `{"extra":1}`, `nope`} {
		if put(bad) != 400 {
			t.Errorf("accepted %s", bad)
		}
	}
	if put(`{"enabled":true,"targets":[{"host":"t.example","port":443}]}`) != 200 {
		t.Fatal("refused a good config")
	}
	reloaded := New(Options{Store: db, Gateways: func() []string { return nil }})
	reloaded.Load(ctx)
	if !reloaded.cfg.Enabled || len(reloaded.cfg.Targets) != 1 {
		t.Fatalf("%+v", reloaded.cfg)
	}
	rows, err := db.AuditBetween(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 5)
	if err != nil || len(rows) != 1 || rows[0].Action != "connectivity.configure" || rows[0].Target != "on" {
		t.Fatalf("%+v %v", rows, err)
	}
	m.tick(ctx)
	if probes == 0 {
		t.Fatal("no checks after turning them on")
	}
	if put(`{"targets":[]}`) != 200 {
		t.Fatal("cannot turn off")
	}
	probes = 0
	m.tick(ctx)
	if probes != 0 {
		t.Fatal("checks after turning them off")
	}
}

func TestTCPCheck(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if _, err := TCPCheck(context.Background(), "127.0.0.1", port); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if _, err := TCPCheck(context.Background(), "127.0.0.1", port); err == nil {
		t.Fatal("a closed port answered")
	}
}

func TestEcho(t *testing.T) {
	b := echoRequest(0x1234)
	if b[0] != 8 || isEchoReply(b, 0x1234) {
		t.Fatalf("%x", b)
	}
	b[0] = 0
	if !isEchoReply(b, 0x1234) || isEchoReply(b, 1) || isEchoReply(b[:4], 0x1234) {
		t.Fatal("reply matching")
	}
}

func TestSummarize(t *testing.T) {
	got := summarize([]store.NetUsage{{Iface: "b", Day: "2026-10-01", Rx: 1}, {Iface: "a", Day: "2026-10-01", Rx: 2}, {Iface: "a", Day: "2026-10-02", Rx: 3, Tx: 4}}, "2026-10-02")
	if len(got) != 2 || got[0].Iface != "a" || got[0].MonthRx != 5 || got[0].TodayRx != 3 || got[0].TodayTx != 4 || len(got[0].Days) != 2 {
		t.Fatalf("%+v", got)
	}
}

func FuzzConfig(f *testing.F) {
	f.Add(`{"enabled":true,"targets":[{"host":"a.example","port":443}],"dns_name":"b.example"}`)
	f.Fuzz(func(t *testing.T, s string) {
		var c Config
		if json.Unmarshal([]byte(s), &c) != nil || c.Validate() != nil {
			return
		}
		for _, x := range c.Targets {
			if strings.ContainsAny(x.Host, "/ \n\x00@") || x.Port < 0 || x.Port > 65535 {
				t.Fatalf("%+v", c)
			}
		}
		if len(c.Targets) > maxTargets || (c.Enabled && len(c.Targets) == 0) {
			t.Fatalf("%+v", c)
		}
	})
}

func FuzzValidHost(f *testing.F) {
	f.Add("example.com")
	f.Add("::1")
	f.Fuzz(func(t *testing.T, s string) {
		if ValidHost(s) && (strings.ContainsAny(s, "/ \n\x00@%") || strings.HasPrefix(s, "-") || len(s) > 253) {
			t.Fatalf("%q", s)
		}
	})
}

// A real ping socket where this user may open one.
func TestPingLoopback(t *testing.T) {
	took, err := PingCheck(context.Background(), "127.0.0.1")
	if errors.Is(err, ErrNoPing) {
		t.Skip("ping_group_range does not include this user")
	}
	if err != nil || took <= 0 {
		t.Fatal(took, err)
	}
}
