package reports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/changes"
	"github.com/mohammed-w-mosabeh/hearth/internal/posture"
	"github.com/mohammed-w-mosabeh/hearth/internal/power"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const evil = `<img src=x onerror=alert(1)>[x](javascript:alert(1))|`

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hearth", "hearth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func metric(t *testing.T, db *store.DB, table, name string, ts time.Time, avg, mx float64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO `+table+` (metric, ts, avg, min, max, n) VALUES (?, ?, ?, ?, ?, 1)`, name, ts.Unix(), avg, avg, mx); err != nil {
		t.Fatal(err)
	}
}

// seed fills a week: hourly CPU, memory and network for the last 84
// hours (half the week), usage for two items, an alert and two actions.
func seed(t *testing.T, db *store.DB) {
	t.Helper()
	ctx := context.Background()
	for h := 1; h <= 84; h++ {
		ts := now.Add(-time.Duration(h) * time.Hour).Truncate(time.Hour)
		metric(t, db, "metrics_1h", "cpu.usage", ts, 20, 90)
		metric(t, db, "metrics_1h", "mem.used", ts, 4<<30, 6<<30)
		metric(t, db, "metrics_1h", "net.rx:wlan0", ts, 1000, 5000)
		metric(t, db, "metrics_1h", "disk.used:/", ts, float64(100<<30+(84-h)<<20), 0)
	}
	cups := "unit:cups.service"
	var rows []store.ItemUsage
	for h := 1; h <= 3; h++ {
		ts := now.Add(-time.Duration(h) * time.Hour).Truncate(time.Hour)
		rows = append(rows, store.ItemUsage{Item: cups, Kind: "service", Name: "cups", Time: ts, CPUAvg: float64(h), CPUMax: float64(10 * h), MemAvg: 1 << 20, MemMax: float64(h) * (1 << 20), N: 60})
	}
	rows = append(rows, store.ItemUsage{Item: "container:0123456789ab", Kind: "container", Name: evil, Time: now.Add(-2 * time.Hour).Truncate(time.Hour), CPUAvg: 50, CPUMax: 80, MemAvg: 1 << 30, MemMax: 1 << 30, N: 60})
	if err := db.AddItemUsage(ctx, rows); err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateAlertRule(ctx, store.AlertRule{Name: "Unit failed", Metric: "unit.failed", Op: ">", Threshold: 0, Severity: "critical", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range []string{"system/cups.service", "system/other.service", evil} {
		if _, err := db.OpenAlertEvent(ctx, store.AlertEvent{RuleID: id, RuleName: "Unit failed", Metric: "unit.failed", Instance: inst, Severity: "critical", Op: ">", Value: 1, Peak: 1, StartedAt: now.Add(-time.Hour).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	cid, err := db.CreateAlertRule(ctx, store.AlertRule{Name: "Container exited", Metric: "container.exited", Op: ">", Threshold: 0, Severity: "warning", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.OpenAlertEvent(ctx, store.AlertEvent{RuleID: cid, RuleName: "Container exited", Metric: "container.exited", Instance: evil, Severity: "warning", Op: ">", Value: 1, Peak: 1, StartedAt: now.Add(-time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"system/cups.service", "system/sshd.service"} {
		if err := db.Audit(ctx, store.AuditEntry{Time: now.Add(-30 * time.Minute), Origin: store.OriginLocal, Action: "unit.restart", Target: target, Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
}

func builder(db *store.DB) *Builder {
	pos := func(context.Context) posture.Report {
		return posture.Report{Checks: []posture.Check{{ID: posture.CheckListeners, Status: posture.SevWarn,
			Findings: []posture.Finding{{Code: "listener.exposed", Severity: posture.SevWarn, Subject: "TCP 8080 (" + evil + ")"}}}}}
	}
	return New(Config{Store: db, Clock: func() time.Time { return now }, Posture: pos, Now: func(context.Context) Now {
		return Now{Host: "box", Disks: []Disk{{Mount: "/", Total: 200 << 30, Used: 101 << 30, Pct: 50.5}},
			Batteries: []power.Battery{{ID: "BAT0", Model: evil, Pct: 80, HealthPct: 91, Cycles: 120}}}
	}})
}

func TestMachineReport(t *testing.T) {
	db := open(t)
	seed(t, db)
	m, err := builder(db).Machine(context.Background(), "week")
	if err != nil {
		t.Fatal(err)
	}
	if m.Host != "box" || !m.From.Equal(now.Add(-7*24*time.Hour)) || m.Covered < 0.49 || m.Covered > 0.51 {
		t.Fatalf("header %+v", m)
	}
	stats := map[string]Stat{}
	for _, s := range m.Stats {
		stats[s.Metric] = s
	}
	if s := stats["cpu.usage"]; s.Avg != 20 || s.Max != 90 || s.Total != nil || s.Unit != "%" {
		t.Errorf("cpu %+v", s)
	}
	// 1000 B/s for 84 hours.
	if s := stats["net.rx:wlan0"]; s.Total == nil || *s.Total != 1000*84*3600 || s.Label != "Network received (wlan0)" {
		t.Errorf("net %+v", s)
	}
	if len(m.Trends) != 2 || len(m.Trends[0].V) != trendSize || m.Trends[0].Max != 100 {
		t.Errorf("trends %+v", m.Trends)
	}
	if g := m.Disks[0].Growth; g == nil || *g != 83<<20 {
		t.Errorf("disk growth %v", g)
	}
	if len(m.Top) != 2 || m.Top[0].Item != "container:0123456789ab" || len(m.Alerts) != 4 || len(m.Changes) != 2 || m.Changes[0].Source != changes.SourceHearth {
		t.Errorf("top %+v alerts %d changes %d", m.Top, len(m.Alerts), len(m.Changes))
	}

	if m.Posture == nil || len(m.Posture.Checks) != 1 {
		t.Errorf("posture %+v", m.Posture)
	}
	if _, err := builder(db).Machine(context.Background(), "year"); !errors.Is(err, ErrPeriod) {
		t.Errorf("bad period: %v", err)
	}
	// A day reads minutes.
	metric(t, db, "metrics_1m", "cpu.usage", now.Add(-time.Minute).Truncate(time.Minute), 40, 50)
	d, err := builder(db).Machine(context.Background(), "day")
	if err != nil || len(d.Stats) != 1 || d.Stats[0].Avg != 40 {
		t.Errorf("day %v %+v", err, d.Stats)
	}
}

func TestItemReport(t *testing.T) {
	db := open(t)
	seed(t, db)
	it, err := builder(db).Item(context.Background(), "unit:cups.service", "day")
	if err != nil {
		t.Fatal(err)
	}
	if it.Name != "cups" || it.Kind != "service" || len(it.Hours) != 3 || it.Total == nil || it.Total.CPUAvg != 2 || it.Total.Hours != 3 {
		t.Fatalf("summary %+v %+v", it, it.Total)
	}
	if it.PeakCPU.CPUMax != 30 || it.PeakMem.MemMax != 3<<20 || len(it.Trends) != 2 || len(it.Trends[0].V) != 24 || it.Trends[0].Peak != 3 {
		t.Errorf("peaks %+v %+v", it.PeakCPU, it.PeakMem)
	}
	if len(it.Events) != 1 || it.Events[0].Instance != "system/cups.service" {
		t.Errorf("events %+v", it.Events)
	}
	if len(it.Changes) != 1 || it.Changes[0].Target != "system/cups.service" {
		t.Errorf("changes %+v", it.Changes)
	}
	c, err := builder(db).Item(context.Background(), "container:0123456789ab", "day")
	if err != nil || len(c.Events) != 1 || c.Events[0].Metric != "container.exited" {
		t.Errorf("container events %v %+v", err, c.Events)
	}
	none, err := builder(db).Item(context.Background(), "unit:never.service", "day")
	if err != nil || none.Total != nil || none.Name != "unit:never.service" || none.Hours == nil {
		t.Errorf("unknown item %v %+v", err, none)
	}
}

var scriptish = regexp.MustCompile(`(?i)<(script|img|iframe|object|a )`)

func TestHTMLIsSelfContainedAndEscaped(t *testing.T) {
	db := open(t)
	seed(t, db)
	b := builder(db)
	m, _ := b.Machine(context.Background(), "week")
	it, _ := b.Item(context.Background(), "container:0123456789ab", "week")
	for name, render := range map[string]func() ([]byte, error){
		"machine": func() ([]byte, error) { return MachineHTML(m) },
		"item":    func() ([]byte, error) { return ItemHTML(it) },
	} {
		page, err := render()
		if err != nil {
			t.Fatal(name, err)
		}
		s := string(page)
		if scriptish.MatchString(s) {
			t.Errorf("%s: unescaped markup", name)
		}
		if name == "machine" && !strings.Contains(s, "listens on every address") {
			t.Errorf("machine: no posture section")
		}
		if !strings.Contains(s, "&lt;img src=x onerror=alert(1)&gt;") {
			t.Errorf("%s: the hostile name is not shown as text", name)
		}
		if strings.Contains(s, "src=\"http") || strings.Contains(s, "href=") || strings.Contains(s, "url(") {
			t.Errorf("%s: loads or links something", name)
		}
		// The inline stylesheet is the one the CSP hash allows.
		css := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(s)
		sum := sha256.Sum256([]byte(css[1]))
		if css == nil || !strings.Contains(html.UnescapeString(s), "sha256-"+base64.StdEncoding.EncodeToString(sum[:])) || strings.Contains(s, "unsafe-inline") {
			t.Errorf("%s: style not allowed by its CSP hash", name)
		}
	}
}

func TestMarkdownEscaped(t *testing.T) {
	db := open(t)
	seed(t, db)
	b := builder(db)
	m, _ := b.Machine(context.Background(), "week")
	it, _ := b.Item(context.Background(), "container:0123456789ab", "week")
	for name, out := range map[string][]byte{"machine": MachineMarkdown(m), "item": ItemMarkdown(it)} {
		s := string(out)
		if regexp.MustCompile(`(^|[^\\])(<img|\]\(javascript)`).MatchString(s) {
			t.Errorf("%s: unescaped markup:\n%s", name, s)
		}
		if !strings.Contains(s, `\<img src=x onerror=alert(1)\>\[x\](javascript\:alert(1))\|`) {
			t.Errorf("%s: hostile name not escaped as expected:\n%s", name, s)
		}
		// Every table row has as many cells as its header.
		var cols int
		for _, line := range strings.Split(s, "\n") {
			if !strings.HasPrefix(line, "| ") {
				cols = 0
				continue
			}
			n := strings.Count(strings.ReplaceAll(line, `\|`, ""), "|")
			if cols == 0 {
				cols = n
			} else if n != cols {
				t.Errorf("%s: ragged row %q", name, line)
			}
		}
	}
}

type memSettings map[string]json.RawMessage

func (m memSettings) Setting(_ context.Context, k string) (json.RawMessage, bool, error) {
	v, ok := m[k]
	return v, ok, nil
}

func (m memSettings) SetSetting(_ context.Context, k string, v json.RawMessage) error {
	m[k] = v
	return nil
}

func TestWeekly(t *testing.T) {
	db := open(t)
	seed(t, db)
	ctx := context.Background()
	clock := now
	w := &Weekly{B: builder(db), Settings: memSettings{}, Dir: filepath.Join(t.TempDir(), "reports"), Clock: func() time.Time { return clock }}
	if err := w.Check(ctx); err != nil || len(w.List()) != 0 {
		t.Fatalf("disabled: %v %v", err, w.List())
	}
	if _, err := w.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := w.Check(ctx); err != nil {
		t.Fatal(err)
	}
	saved := w.List()
	if len(saved) != 1 || saved[0].Name != "hearth-week-"+now.Local().Format("2006-01-02")+".html" {
		t.Fatalf("saved %+v", saved)
	}
	if fi, _ := os.Stat(filepath.Join(w.Dir, saved[0].Name)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(w.Dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	// Not due again for a week.
	clock = now.Add(6 * 24 * time.Hour)
	if _, err := w.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := w.Check(ctx); err != nil || len(w.List()) != 1 {
		t.Fatalf("early: %v %+v", err, w.List())
	}
	for i := 1; i <= 14; i++ {
		clock = now.Add(time.Duration(i) * 7 * 24 * time.Hour)
		if err := w.Check(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(w.List()); n != weeklyKeep {
		t.Errorf("kept %d", n)
	}
	if b, ok := w.Read(w.List()[0].Name); !ok || !bytes.HasPrefix(b, []byte("<!doctype html>")) {
		t.Error("cannot read the newest report")
	}
	if err := os.WriteFile(filepath.Join(w.Dir, "notes.html"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"notes.html", "../hearth.db", "hearth-week-2026-10-29.html/../x"} {
		if _, ok := w.Read(bad); ok {
			t.Errorf("read %q", bad)
		}
	}
}

type mux struct{ *http.ServeMux }

func (m mux) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

type audit struct{ entries []store.AuditEntry }

func (a *audit) Audit(_ context.Context, e store.AuditEntry) error {
	a.entries = append(a.entries, e)
	return nil
}

func TestHandlers(t *testing.T) {
	db := open(t)
	seed(t, db)
	b := builder(db)
	au := &audit{}
	w := &Weekly{B: b, Settings: memSettings{}, Dir: filepath.Join(t.TempDir(), "reports"), Clock: b.cfg.Clock}
	m := mux{http.NewServeMux()}
	(&API{B: b, Weekly: w, Audit: au}).Register(m)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
		return rec
	}
	for target, want := range map[string]int{
		"/api/v1/reports/machine?period=week":                  200,
		"/api/v1/reports/machine?period=decade":                400,
		"/api/v1/reports/machine?period=day&format=pdf":        400,
		"/api/v1/reports/item?id=unit:cups.service&period=day": 200,
		"/api/v1/reports/item?id=exe:/bin/sh&period=day":       400,
		"/api/v1/reports/item?id=unit:a%0Ab&period=day":        400,
		"/api/v1/reports/items":                                200,
		"/api/v1/reports/weekly":                               200,
		"/api/v1/reports/saved/hearth-week-2026-10-29.html":    404,
		"/api/v1/reports/saved/..%2Fhearth.db":                 404,
	} {
		if rec := do("GET", target, ""); rec.Code != want {
			t.Errorf("%s: %d %s, want %d", target, rec.Code, rec.Body, want)
		}
	}
	rec := do("GET", "/api/v1/reports/machine?period=week&format=html", "")
	if rec.Code != 200 || rec.Header().Get("Content-Disposition") != `attachment; filename="hearth-week-2026-10-29.html"` || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("html: %d %v", rec.Code, rec.Header())
	}
	rec = do("GET", "/api/v1/reports/item?id=unit:cups.service&period=week&format=md", "")
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "# cups") {
		t.Errorf("md: %d %s", rec.Code, rec.Body)
	}
	var items []store.ItemTotal
	if err := json.Unmarshal(do("GET", "/api/v1/reports/items", "").Body.Bytes(), &items); err != nil || len(items) != 2 {
		t.Errorf("items %v %+v", err, items)
	}

	for _, body := range []string{"", `{}`, `{"enabled": "yes"}`, `{"enabled": true, "x": 1}`} {
		if rec := do("POST", "/api/v1/reports/weekly", body); rec.Code != 400 {
			t.Errorf("POST %q: %d", body, rec.Code)
		}
	}
	if rec := do("POST", "/api/v1/reports/weekly", `{"enabled": false}`); rec.Code != 200 {
		t.Errorf("disable: %d %s", rec.Code, rec.Body)
	}
	if len(au.entries) != 1 || au.entries[0].Action != "reports.weekly" {
		t.Errorf("audit %+v", au.entries)
	}
	if err := w.Check(context.Background()); err != nil || len(w.List()) != 0 {
		t.Errorf("disabled yet saved: %v", err)
	}

	// Turning it on saves the first report at once, through Run.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, time.Hour); close(done) }()
	defer func() { cancel(); <-done }()
	if rec := do("POST", "/api/v1/reports/weekly", `{"enabled": true}`); rec.Code != 200 {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
	for deadline := time.Now().Add(10 * time.Second); len(w.List()) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no report after enabling")
		}
	}
	name := w.List()[0].Name
	rec = do("GET", "/api/v1/reports/saved/"+name, "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), name) || !strings.Contains(rec.Body.String(), "Machine report") {
		t.Errorf("saved: %d %v", rec.Code, rec.Header())
	}
}

func TestMachineReportTimeline(t *testing.T) {
	db := open(t)
	seed(t, db)
	b := builder(db)
	b.cfg.Timeline = func(_ context.Context, _, _ time.Time, limit int) ([]changes.Event, error) {
		if limit != maxChanges {
			t.Errorf("limit %d", limit)
		}
		return []changes.Event{
			{Time: now, Source: changes.SourcePackages, Kind: changes.KindUpgraded, Subject: evil, Before: "1", After: "2"},
			{Time: now, Source: changes.SourceUnits, Kind: changes.KindAdded, Subject: "system/x.service", After: evil},
			{Time: now, Source: changes.SourceHearth, Kind: changes.KindAction, Subject: "y", Action: "unit.restart", Origin: "local", Result: "ok"},
		}, nil
	}
	m, err := b.Machine(context.Background(), "week")
	if err != nil {
		t.Fatal(err)
	}
	html, err := MachineHTML(m)
	if err != nil {
		t.Fatal(err)
	}
	mdText := string(MachineMarkdown(m))
	for _, want := range []string{"Package upgraded", "Unit added", "Hearth: unit.restart", "1 → 2", "ok (local)"} {
		if !strings.Contains(string(html), want) || !strings.Contains(strings.ReplaceAll(mdText, `\`, ""), want) {
			t.Errorf("%q missing", want)
		}
	}
	for _, key := range []string{"src.", "ev."} {
		if strings.Contains(string(html), key+"packages") || strings.Contains(mdText, key+"upgraded") {
			t.Errorf("an untranslated %s key", key)
		}
	}
	if scriptish.MatchString(string(html)) {
		t.Error("the timeline is not escaped")
	}
}

func TestMarkLines(t *testing.T) {
	tr := Trend{T: []int64{1000, 2000, 3000}, Marks: []int64{500, 1000, 2000, 3000, 3500}}
	got := markLines(tr)
	if len(got) != 3 || got[0] != "0.0" || got[1] != "300.0" || got[2] != "600.0" {
		t.Fatalf("%v", got)
	}
	if markLines(Trend{T: []int64{1000}, Marks: []int64{1000}}) != nil {
		t.Fatal("one point has no width")
	}
}

func TestMachineApps(t *testing.T) {
	db := open(t)
	seed(t, db)
	b := builder(db)
	m, _ := b.Machine(context.Background(), "week")
	if m.Apps != nil {
		t.Fatalf("apps without a source: %+v", m.Apps)
	}
	// Every heading and status has its English words (they were keys).
	if out := string(MachineMarkdown(m)); !strings.Contains(out, "## Security posture") || strings.Contains(out, "status.") {
		t.Errorf("posture labels missing:\n%s", out)
	}
	hostile := "<img src=x onerror=alert(1)>"
	for _, tc := range []struct {
		apps     []App
		html, md []string
		notHTML  string
	}{
		{apps: nil, notHTML: "<h2>Apps</h2>"},
		{apps: []App{}, html: []string{"<h2>Apps</h2>", "Hearth manages no app."}, md: []string{"## Apps", "_Hearth manages no app._"}},
		{apps: []App{
			{Name: "cloud", State: "running", Unhealthy: []string{hostile}, Backup: "2026-11-06T03:00:00Z", Scheduled: true, Updates: 2},
			{Name: "wiki", State: "stopped", Unhealthy: []string{}},
		}, html: []string{"&lt;img src=x onerror=alert(1)&gt;", "2026-11-06T03:00:00Z · failed", "never", "<td class=\"n\">2</td>"},
			md: []string{"| cloud | running | \\<img src=x onerror=alert(1)\\> | 2026-11-06T03\\:00\\:00Z · failed | yes | 2 |", "| wiki | stopped |  | never | no |  |"}},
	} {
		m.Apps = tc.apps
		page, err := MachineHTML(m)
		if err != nil {
			t.Fatal(err)
		}
		s := string(page)
		for _, want := range tc.html {
			if !strings.Contains(s, want) {
				t.Errorf("html for %v: no %q", tc.apps, want)
			}
		}
		if tc.notHTML != "" && strings.Contains(s, tc.notHTML) {
			t.Errorf("html for nil apps has %q", tc.notHTML)
		}
		out := string(MachineMarkdown(m))
		for _, want := range tc.md {
			if !strings.Contains(out, want) {
				t.Errorf("markdown for %v: no %q in\n%s", tc.apps, want, out)
			}
		}
	}
	b2 := New(Config{Store: db, Apps: func(context.Context) ([]App, error) { return []App{{Name: "x"}}, nil }})
	if m2, _ := b2.Machine(context.Background(), "week"); len(m2.Apps) != 1 {
		t.Errorf("apps source not read: %+v", m2.Apps)
	}
}
