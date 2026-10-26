package alerts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/power"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// memStore is an in-memory store.DB for the engine and the API.
type memStore struct {
	mu     sync.Mutex
	rules  []store.AlertRule
	events []store.AlertEvent
	audits []store.AuditEntry
}

func (m *memStore) AlertRules(context.Context) ([]store.AlertRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.AlertRule{}, m.rules...), nil
}
func (m *memStore) CreateAlertRule(_ context.Context, r store.AlertRule) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = int64(len(m.rules) + 100)
	m.rules = append(m.rules, r)
	return r.ID, nil
}
func (m *memStore) UpdateAlertRule(_ context.Context, r store.AlertRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rules {
		if m.rules[i].ID == r.ID {
			m.rules[i] = r
			return nil
		}
	}
	return store.ErrNotFound
}
func (m *memStore) DeleteAlertRule(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rules {
		if m.rules[i].ID == id {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}
func (m *memStore) OpenAlertEvent(_ context.Context, e store.AlertEvent) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.ID = int64(len(m.events) + 1)
	m.events = append(m.events, e)
	return e.ID, nil
}
func (m *memStore) CloseAlertEvent(_ context.Context, id int64, ended int64, peak float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.events {
		if m.events[i].ID == id && m.events[i].EndedAt == nil {
			m.events[i].EndedAt, m.events[i].Peak = &ended, peak
			return nil
		}
	}
	return store.ErrNotFound
}
func (m *memStore) OpenAlertEvents(context.Context) ([]store.AlertEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AlertEvent
	for _, e := range m.events {
		if e.EndedAt == nil {
			out = append(out, e)
		}
	}
	return out, nil
}
func (m *memStore) AlertEvents(_ context.Context, limit int, _ int64) ([]store.AlertEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]store.AlertEvent{}, m.events...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *memStore) Audit(_ context.Context, e store.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, e)
	return nil
}

type notes struct {
	mu   sync.Mutex
	sent []string
}

func (n *notes) Notify(_ context.Context, summary, body, severity string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, severity+": "+summary+": "+body)
	return nil
}
func (n *notes) list() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string{}, n.sent...)
}

type pubs struct {
	mu   sync.Mutex
	msgs []Msg
}

func (p *pubs) Publish(_ string, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, data.(Msg))
	return nil
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func rule(id int64, metric, op string, threshold float64, forS int) store.AlertRule {
	return store.AlertRule{ID: id, Name: "R" + metric, Metric: metric, Op: op, Threshold: threshold, ForSec: forS, Severity: "warning", Enabled: true}
}

func TestValidate(t *testing.T) {
	ok := rule(1, "disk.used_pct", ">", 90, 60)
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(r *store.AlertRule){
		"metric":     func(r *store.AlertRule) { r.Metric = "rm -rf" },
		"name":       func(r *store.AlertRule) { r.Name = " " },
		"name ctl":   func(r *store.AlertRule) { r.Name = "a\nb" },
		"op":         func(r *store.AlertRule) { r.Op = ">=" },
		"threshold":  func(r *store.AlertRule) { r.Threshold = 101 },
		"for":        func(r *store.AlertRule) { r.ForSec = -1 },
		"severity":   func(r *store.AlertRule) { r.Severity = "panic" },
		"quiet half": func(r *store.AlertRule) { r.QuietFrom = "22:00" },
		"quiet fmt":  func(r *store.AlertRule) { r.QuietFrom, r.QuietTo = "25:00", "07:00" },
		"quiet same": func(r *store.AlertRule) { r.QuietFrom, r.QuietTo = "07:00", "07:00" },
		"target":     func(r *store.AlertRule) { r.Metric, r.Target = "cpu.usage", "x" },
	} {
		r := ok
		mod(&r)
		if Validate(r) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestQuiet(t *testing.T) {
	r := rule(1, "cpu.usage", ">", 90, 0)
	r.QuietFrom, r.QuietTo = "22:00", "07:00"
	at := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, time.Local) }
	for _, c := range []struct {
		h, m int
		want bool
	}{{23, 0, true}, {2, 0, true}, {6, 59, true}, {7, 0, false}, {12, 0, false}, {22, 0, true}, {21, 59, false}} {
		if quiet(r, at(c.h, c.m)) != c.want {
			t.Errorf("%02d:%02d: %v", c.h, c.m, !c.want)
		}
	}
	r.QuietFrom, r.QuietTo = "12:00", "14:00"
	if !quiet(r, at(13, 0)) || quiet(r, at(14, 0)) {
		t.Error("daytime window")
	}
}

func newEngine(t *testing.T, rules ...store.AlertRule) (*Engine, *memStore, *notes, *pubs) {
	t.Helper()
	ms := &memStore{rules: rules}
	n, p := &notes{}, &pubs{}
	e := NewEngine(ms, n, p, quietLog)
	if err := e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e, ms, n, p
}

func TestEngineDurationHysteresisAndOneNotification(t *testing.T) {
	e, ms, n, _ := newEngine(t, rule(1, "mem.used_pct", ">", 90, 300))
	ctx := context.Background()
	t0 := time.Unix(1_800_000_000, 0)
	obs := func(sec int, v float64) {
		e.Observe(ctx, t0.Add(time.Duration(sec)*time.Second), "mem.used_pct", map[string]float64{"": v})
	}

	obs(0, 95)
	obs(200, 96)
	obs(250, 80) // dropped below before 5 min: pending ends
	obs(260, 95)
	obs(500, 97) // 240 s since 260: not yet
	if len(ms.events) != 0 {
		t.Fatalf("fired early: %+v", ms.events)
	}
	obs(560, 98) // 300 s
	obs(600, 99)
	obs(620, 88) // below 90 but within hysteresis (85): still firing
	obs(630, 93)
	e.Wait()
	if len(ms.events) != 1 || ms.events[0].Value != 98 || ms.events[0].EndedAt != nil || len(n.list()) != 1 {
		t.Fatalf("events %+v notes %v", ms.events, n.list())
	}
	obs(640, 85) // cleared
	if ms.events[0].EndedAt == nil || ms.events[0].Peak != 99 {
		t.Fatalf("not resolved: %+v", ms.events[0])
	}
	if got := n.list()[0]; got != "warning: Rmem.used_pct: Memory used is 98%, above 90%." {
		t.Errorf("note %q", got)
	}
}

func TestEngineInstancesTargetsAndAbsence(t *testing.T) {
	low := rule(2, "battery.discharging", "<", 15, 0)
	home := rule(3, "disk.used_pct", ">", 80, 0)
	home.Target = "/home"
	e, ms, n, p := newEngine(t, rule(1, "disk.used_pct", ">", 90, 0), low, home)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	e.Observe(ctx, now, "disk.used_pct", map[string]float64{"/": 95, "/home": 85, "/boot": 10})
	e.Observe(ctx, now, "battery.discharging", map[string]float64{"BAT0": 12})
	e.Wait()
	got := map[string]int{}
	for _, ev := range ms.events {
		got[ev.Metric+" "+ev.Instance]++
	}
	if len(ms.events) != 3 || got["disk.used_pct /"] != 1 || got["disk.used_pct /home"] != 1 || got["battery.discharging BAT0"] != 1 || len(n.list()) != 3 {
		t.Fatalf("events %+v", ms.events)
	}
	// Plugged in: the battery is no longer discharging; the incident ends.
	e.Observe(ctx, now.Add(time.Minute), "battery.discharging", map[string]float64{})
	if ms.events[2].EndedAt == nil {
		t.Fatal("absent instance did not resolve")
	}
	if len(p.msgs) != 4 || p.msgs[3].Kind != "close" {
		t.Fatalf("published %+v", p.msgs)
	}
	if inst := e.Instances("disk.used_pct"); strings.Join(inst, ",") != "/,/boot,/home" {
		t.Errorf("instances %v", inst)
	}
}

func TestEngineQuietHoursRecordWithoutNotifying(t *testing.T) {
	r := rule(1, "cpu.usage", ">", 95, 0)
	r.QuietFrom, r.QuietTo = "00:00", "23:59"
	e, ms, n, _ := newEngine(t, r)
	e.Observe(context.Background(), time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local), "cpu.usage", map[string]float64{"": 99})
	e.Wait()
	if len(ms.events) != 1 || ms.events[0].Notified || len(n.list()) != 0 {
		t.Fatalf("events %+v notes %v", ms.events, n.list())
	}
}

func TestEngineResumeAndReload(t *testing.T) {
	ms := &memStore{rules: []store.AlertRule{rule(1, "unit.failed", ">", 0, 0), rule(2, "cpu.usage", ">", 95, 0)}}
	ms.events = []store.AlertEvent{{ID: 1, RuleID: 1, Metric: "unit.failed", Instance: "system/x.service", Op: ">", Value: 1, Peak: 1, StartedAt: 1}}
	n := &notes{}
	e := NewEngine(ms, n, nil, quietLog)
	if err := e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	// Still failed after a restart: same incident, no new notification.
	e.Observe(ctx, now, "unit.failed", map[string]float64{"system/x.service": 1, "user/y.service": 1})
	e.Observe(ctx, now, "cpu.usage", map[string]float64{"": 99})
	e.Wait()
	if len(ms.events) != 3 || len(n.list()) != 2 || ms.events[0].EndedAt != nil {
		t.Fatalf("events %+v notes %v", ms.events, n.list())
	}
	if !strings.HasSuffix(n.list()[0], "user/y.service has failed.") && !strings.HasSuffix(n.list()[1], "user/y.service has failed.") {
		t.Errorf("notes %v", n.list())
	}
	// Editing the CPU rule closes its incident; the unit rule's stays.
	ms.rules[1].Threshold = 97
	if err := e.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if ms.events[2].EndedAt == nil || ms.events[0].EndedAt != nil {
		t.Fatalf("after reload %+v", ms.events)
	}
	// Disabling the unit rule closes the rest.
	ms.rules[0].Enabled = false
	_ = e.Reload(ctx)
	if ms.events[0].EndedAt == nil || ms.events[1].EndedAt == nil {
		t.Fatalf("after disable %+v", ms.events)
	}
	// A resumed incident of a deleted rule is closed at load.
	ms2 := &memStore{events: []store.AlertEvent{{ID: 1, RuleID: 9, StartedAt: 1}}}
	_ = NewEngine(ms2, nil, nil, quietLog).Load(ctx)
	if ms2.events[0].EndedAt == nil {
		t.Fatal("orphan incident left open")
	}
}

func TestFromSample(t *testing.T) {
	temp, gpuTemp := 71.0, 64.0
	s := collect.Sample{
		CPU:      collect.CPU{Usage: 12},
		Mem:      collect.Mem{Total: 1000, Used: 250, SwapTotal: 0},
		Disks:    []collect.Disk{{Mount: "/", Pct: 41}},
		GPUs:     []collect.GPU{{PCI: "0000:00:02.0", TempC: &gpuTemp}, {PCI: "0000:01:00.0"}},
		CPUTempC: &temp,
		Power:    power.State{Batteries: []power.Battery{{ID: "BAT0", Pct: 50, State: "discharging"}, {ID: "BAT1", Pct: 90, State: "charging"}}},
	}
	got := FromSample(s)
	if got["cpu.usage"][""] != 12 || got["mem.used_pct"][""] != 25 || len(got["swap.used_pct"]) != 0 || got["disk.used_pct"]["/"] != 41 ||
		got["cpu.temp"][""] != 71 || len(got["gpu.temp"]) != 1 || got["gpu.temp"]["0000:00:02.0"] != 64 ||
		len(got["battery.discharging"]) != 1 || got["battery.discharging"]["BAT0"] != 50 {
		t.Fatalf("%v", got)
	}
	s.Partial = true
	if _, ok := FromSample(s)["cpu.usage"]; ok {
		t.Fatal("partial sample reported CPU usage")
	}
}

func TestMarkup(t *testing.T) {
	if got := markup.Replace(`<b>x</b> & <a href="y">`); got != `&lt;b&gt;x&lt;/b&gt; &amp; &lt;a href="y"&gt;` {
		t.Fatal(got)
	}
}

func TestAPI(t *testing.T) {
	ms := &memStore{rules: []store.AlertRule{rule(1, "cpu.usage", ">", 95, 180)}}
	n := &notes{}
	e := NewEngine(ms, n, nil, quietLog)
	_ = e.Load(context.Background())
	mux := http.NewServeMux()
	(&API{Store: ms, Engine: e, Notify: n}).Register(mux)
	do := func(method, path, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	good := `{"name":"Home full","metric":"disk.used_pct","target":"/home","op":">","threshold":85,"for_s":0,"severity":"info","enabled":true,"quiet_from":"","quiet_to":""}`
	code, out := do("POST", "/api/v1/alerts/rules", good)
	if code != 200 || out["id"] == nil {
		t.Fatalf("create %d %v", code, out)
	}
	id := int64(out["id"].(float64))
	if code, _ := do("POST", "/api/v1/alerts/rules", strings.Replace(good, `"op":">"`, `"op":"!"`, 1)); code != 400 {
		t.Errorf("bad op %d", code)
	}
	if code, _ := do("POST", "/api/v1/alerts/rules", strings.Replace(good, `"enabled":true`, `"enabled":true,"exec":"x"`, 1)); code != 400 {
		t.Errorf("unknown field %d", code)
	}
	if code, _ := do("PUT", "/api/v1/alerts/rules/"+strconv.FormatInt(id, 10), strings.Replace(good, "85", "88", 1)); code != 200 || ms.rules[1].Threshold != 88 {
		t.Errorf("update %d %+v", code, ms.rules)
	}
	if code, _ := do("PUT", "/api/v1/alerts/rules/999", good); code != 404 {
		t.Errorf("update missing %d", code)
	}
	if code, _ := do("DELETE", "/api/v1/alerts/rules/"+strconv.FormatInt(id, 10), ""); code != 204 || len(ms.rules) != 1 {
		t.Errorf("delete %d", code)
	}
	if code, _ := do("DELETE", "/api/v1/alerts/rules/abc", ""); code != 400 {
		t.Errorf("bad id %d", code)
	}
	if len(ms.audits) != 3 || ms.audits[0].Action != "alerts.rule.create" || ms.audits[2].Action != "alerts.rule.delete" {
		t.Errorf("audits %+v", ms.audits)
	}
	if code, out := do("GET", "/api/v1/alerts/metrics", ""); code != 200 || len(out["metrics"].([]any)) != len(Metrics) {
		t.Errorf("metrics %d", code)
	}
	if code, _ := do("GET", "/api/v1/alerts/events?limit=0", ""); code != 400 {
		t.Errorf("limit 0 %d", code)
	}
	if code, _ := do("POST", "/api/v1/alerts/test", ""); code != 204 || len(n.list()) != 1 {
		t.Errorf("test %d", code)
	}
}
