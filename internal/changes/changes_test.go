package changes

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)

func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hearth", "hearth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// tracker reads the given state, which tests change between snapshots.
func tracker(t *testing.T, db *store.DB, state map[string]map[string]string) *Tracker {
	t.Helper()
	readers := map[string]Reader{}
	for _, src := range Snapshotted {
		readers[src] = func(context.Context) (map[string]string, error) {
			m, ok := state[src]
			if !ok {
				return nil, errors.New("unreadable")
			}
			return m, nil
		}
	}
	return New(Config{Store: db, Readers: readers, LogDir: t.TempDir(), Clock: func() time.Time { return now }})
}

func fullState() map[string]map[string]string {
	return map[string]map[string]string{
		SourcePackages:   {"vim:amd64": "1.0"},
		SourceUnits:      {"system/cups.service": "enabled"},
		SourceContainers: {"web": "nginx:1"},
		SourceListeners:  {"TCP 631 (cupsd)": "127.0.0.1"},
	}
}

func TestParseDpkgLine(t *testing.T) {
	for line, want := range map[string]Event{
		"2026-10-29 17:05:12 install vim:amd64 <none> 2:9.1-1": {Kind: KindInstalled, Subject: "vim:amd64", After: "2:9.1-1"},
		"2026-10-29 17:05:12 install vim:amd64 1.0 2.0":        {Kind: KindUpgraded, Subject: "vim:amd64", Before: "1.0", After: "2.0"},
		"2026-10-29 17:05:12 install vim:amd64 1.0 1.0":        {Kind: KindInstalled, Subject: "vim:amd64", Before: "1.0", After: "1.0"},
		"2026-10-29 17:05:12 upgrade vim:amd64 1.0 2.0":        {Kind: KindUpgraded, Subject: "vim:amd64", Before: "1.0", After: "2.0"},
		"2026-10-29 17:05:12 remove vim:amd64 2.0 <none>":      {Kind: KindRemoved, Subject: "vim:amd64", Before: "2.0"},
		"2026-10-29 17:05:12 purge vim:amd64 2.0 <none>":       {Kind: KindPurged, Subject: "vim:amd64", Before: "2.0"},
	} {
		e, ok := parseDpkgLine(line)
		if !ok || e.Kind != want.Kind || e.Subject != want.Subject || e.Before != want.Before || e.After != want.After || e.Source != SourcePackages {
			t.Errorf("%s: %v %+v", line, ok, e)
		}
	}
	for _, line := range []string{
		"", "2026-10-29 17:05:12 startup archives unpack",
		"2026-10-29 17:05:12 status installed vim:amd64 1.0",
		"2026-10-29 17:05:12 configure vim:amd64 1.0 <none>",
		"2026-13-99 17:05:12 install vim:amd64 <none> 1.0",
	} {
		if _, ok := parseDpkgLine(line); ok {
			t.Errorf("parsed %q", line)
		}
	}
}

func TestDpkgLogRotationAndCache(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("dpkg.log", "2026-10-02 10:00:00 upgrade a:amd64 1 2\n")
	write("dpkg.log.1", "2026-09-02 10:00:00 install a:amd64 <none> 1\n")
	write("dpkg.log.old", "2026-01-01 10:00:00 install no:amd64 <none> 1\n")
	var gz strings.Builder
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("2026-08-02 10:00:00 install b:amd64 <none> 1\n"))
	_ = zw.Close()
	write("dpkg.log.2.gz", gz.String())
	write("dpkg.log.3.gz", "not gzip")

	d := &dpkgLog{dir: dir}
	evs := d.Events()
	if len(evs) != 3 || evs[0].Subject != "b:amd64" || evs[2].Kind != KindUpgraded {
		t.Fatalf("%+v", evs)
	}
	write("dpkg.log", "2026-10-02 10:00:00 upgrade a:amd64 1 2\n2026-10-03 10:00:00 remove a:amd64 2 <none>\n")
	if evs := d.Events(); len(evs) != 4 || evs[3].Kind != KindRemoved {
		t.Fatalf("the cache did not notice the new line: %+v", evs)
	}
}

func TestDiff(t *testing.T) {
	a := State{Sections: map[string]map[string]string{SourceUnits: {"a": "enabled", "b": "enabled", "c": "disabled"}, SourcePackages: {}}, Missing: []string{SourceContainers}}
	b := State{Sections: map[string]map[string]string{SourceUnits: {"a": "enabled", "c": "enabled", "d": "static"}, SourcePackages: {}, SourceContainers: {"web": "nginx"}}}
	var units, containers SectionDiff
	for _, d := range Diff(a, b) {
		switch d.Source {
		case SourceUnits:
			units = d
		case SourceContainers:
			containers = d
		}
	}
	if len(units.Added) != 1 || units.Added[0].Key != "d" || len(units.Removed) != 1 || units.Removed[0].Before != "enabled" ||
		len(units.Changed) != 1 || units.Changed[0].Before != "disabled" || units.Changed[0].After != "enabled" {
		t.Errorf("units %+v", units)
	}
	// What could not be read is not all added or all removed.
	if !containers.Missing || !containers.Empty() {
		t.Errorf("containers %+v", containers)
	}
	if Same(a, b) || !Same(a, a) {
		t.Error("Same")
	}
	// Same contents, but a source became readable: worth a new base.
	c := State{Sections: map[string]map[string]string{SourceUnits: {}, SourcePackages: {}}, Missing: []string{SourceContainers}}
	d := State{Sections: map[string]map[string]string{SourceUnits: {}, SourcePackages: {}, SourceContainers: {}}}
	if Same(c, d) {
		t.Error("a source coming back should count as a change")
	}
}

func TestEncodeDecode(t *testing.T) {
	s := State{Sections: map[string]map[string]string{SourceUnits: {"a": "b"}}, Missing: []string{SourceListeners}}
	data, err := encode(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decode(data)
	if err != nil || got.Sections[SourceUnits]["a"] != "b" || got.Missing[0] != SourceListeners {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := decode([]byte("not gzip")); err == nil {
		t.Error("decoded garbage")
	}
}

func TestTakeAutomatic(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	state := fullState()
	tr := tracker(t, db, state)

	if _, stored, err := tr.Take(ctx, "", true); err != nil || !stored {
		t.Fatalf("first: %v %v", stored, err)
	}
	if _, stored, err := tr.Take(ctx, "", true); err != nil || stored {
		t.Fatalf("unchanged: %v %v", stored, err)
	}
	if list, _ := db.Snapshots(ctx); len(list) != 1 {
		t.Fatalf("snapshots %+v", list)
	}

	state[SourcePackages] = map[string]string{"vim:amd64": "2.0"} // left to dpkg's log
	state[SourceUnits] = map[string]string{"system/cups.service": "disabled", "system/new.service": "enabled"}
	delete(state[SourceContainers], "web")
	delete(state, SourceListeners) // unreadable: not all closed
	if _, stored, err := tr.Take(ctx, "", true); err != nil || !stored {
		t.Fatalf("changed: %v %v", stored, err)
	}
	cs, err := db.ChangesBetween(ctx, time.UnixMilli(0), now.Add(time.Hour), nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.Change{}
	for _, c := range cs {
		got[c.Source+"/"+c.Kind+"/"+c.Subject] = c
	}
	if len(cs) != 3 {
		t.Fatalf("changes %+v", cs)
	}
	if c, ok := got[SourceUnits+"/"+KindChanged+"/system/cups.service"]; !ok || c.Before != "enabled" || c.After != "disabled" {
		t.Errorf("%+v", got)
	}
	if _, ok := got[SourceUnits+"/"+KindAdded+"/system/new.service"]; !ok {
		t.Errorf("%+v", got)
	}
	if c, ok := got[SourceContainers+"/"+KindDeleted+"/web"]; !ok || c.Before != "nginx:1" {
		t.Errorf("%+v", got)
	}
}

// A snapshot the user names is stored unchanged or not, and is not the
// base of the automatic diff.
func TestTakeManual(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	tr := tracker(t, db, fullState())
	s, stored, err := tr.Take(ctx, "before", false)
	if err != nil || !stored || s.ID == 0 || s.Size == 0 {
		t.Fatalf("%v %v %+v", stored, err, s)
	}
	if _, stored, _ := tr.Take(ctx, "again", false); !stored {
		t.Error("manual snapshots are always stored")
	}
	if _, err := db.LatestAutoSnapshot(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("latest auto: %v", err)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	db := open(t)
	tr := tracker(t, db, fullState())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Run(ctx, time.Hour); close(done) }()
	for i := 0; ; i++ {
		if list, _ := db.Snapshots(context.Background()); len(list) == 1 {
			break
		}
		if i > 500 {
			t.Fatal("no snapshot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

func addChanges(t *testing.T, db *store.DB, cs ...store.Change) {
	t.Helper()
	if _, err := db.AddSnapshot(context.Background(), store.Snapshot{Time: now, Data: []byte("x")}, cs); err != nil {
		t.Fatal(err)
	}
}

func TestTimelineMergesSources(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	tr := tracker(t, db, fullState())
	line := now.Add(-3*time.Hour).Format("2006-01-02 15:04:05") + " upgrade vim:amd64 1 2\n"
	if err := os.WriteFile(filepath.Join(tr.cfg.LogDir, "dpkg.log"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	addChanges(t, db, store.Change{Time: now.Add(-2 * time.Hour), Source: SourceUnits, Kind: KindAdded, Subject: "system/x.service", After: "enabled"})
	if err := db.Audit(ctx, store.AuditEntry{Time: now.Add(-time.Hour), Origin: store.OriginLocal, Action: "unit.restart", Target: "system/x.service", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	evs, next, err := tr.Timeline(ctx, time.UnixMilli(0), now, nil, 10)
	if err != nil || next != nil || len(evs) != 3 {
		t.Fatalf("%v %v %+v", err, next, evs)
	}
	if evs[0].Source != SourceHearth || evs[0].Action != "unit.restart" || evs[1].Source != SourceUnits || evs[2].Source != SourcePackages {
		t.Errorf("%+v", evs)
	}
	for src, n := range map[string]int{SourcePackages: 1, SourceUnits: 1, SourceHearth: 1, SourceContainers: 0} {
		evs, _, err := tr.Timeline(ctx, time.UnixMilli(0), now, []string{src}, 10)
		if err != nil || len(evs) != n {
			t.Errorf("%s: %v %+v", src, err, evs)
		}
	}
	if evs, _, err := tr.Timeline(ctx, now.Add(-90*time.Minute), now, nil, 10); err != nil || len(evs) != 1 {
		t.Errorf("window: %v %+v", err, evs)
	}
}

// Filtering reaches rows older than any number of newer rows of another
// source.
func TestTimelineFilterReachesOldRows(t *testing.T) {
	db := open(t)
	tr := tracker(t, db, fullState())
	cs := []store.Change{{Time: now.Add(-time.Hour), Source: SourceUnits, Kind: KindAdded, Subject: "system/old.service"}}
	for i := range 40 {
		cs = append(cs, store.Change{Time: now.Add(-time.Duration(i) * time.Minute), Source: SourceContainers, Kind: KindAdded, Subject: strconv.Itoa(i)})
	}
	addChanges(t, db, cs...)
	evs, _, err := tr.Timeline(context.Background(), time.UnixMilli(0), now.Add(time.Minute), []string{SourceUnits}, 2)
	if err != nil || len(evs) != 1 || evs[0].Subject != "system/old.service" {
		t.Fatalf("%v %+v", err, evs)
	}
}

// Paging over a whole upgrade logged in one second neither repeats nor
// loses events.
func TestTimelinePages(t *testing.T) {
	db := open(t)
	tr := tracker(t, db, fullState())
	var log strings.Builder
	stamp := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02 15:04:05") }
	for i := range 3 {
		fmt.Fprintf(&log, "%s install n%d:amd64 <none> 1\n", stamp(time.Hour), i)
	}
	for i := range 4 {
		fmt.Fprintf(&log, "%s install same%d:amd64 <none> 1\n", stamp(time.Minute), i)
	}
	for i := range 3 {
		fmt.Fprintf(&log, "%s install o%d:amd64 <none> 1\n", stamp(2*time.Hour), i)
	}
	if err := os.WriteFile(filepath.Join(tr.cfg.LogDir, "dpkg.log"), []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	to := now
	pages := 0
	for {
		evs, next, err := tr.Timeline(context.Background(), time.UnixMilli(0), to, nil, 5)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, e := range evs {
			seen[e.Subject]++
		}
		if next == nil {
			break
		}
		if pages > 10 {
			t.Fatal("no end")
		}
		to = *next
	}
	if len(seen) != 10 {
		t.Fatalf("%d events: %v", len(seen), seen)
	}
	for s, n := range seen {
		if n != 1 {
			t.Errorf("%s seen %d times", s, n)
		}
	}
}

// More events at one time than a page: a full page, then on before it.
func TestTimelineHugeSecondEnds(t *testing.T) {
	db := open(t)
	tr := tracker(t, db, fullState())
	var log strings.Builder
	for i := range 8 {
		fmt.Fprintf(&log, "%s install n%d:amd64 <none> 1\n", now.Add(-time.Hour).Format("2006-01-02 15:04:05"), i)
	}
	if err := os.WriteFile(filepath.Join(tr.cfg.LogDir, "dpkg.log"), []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	evs, next, err := tr.Timeline(context.Background(), time.UnixMilli(0), now, nil, 5)
	if err != nil || len(evs) != 5 || next == nil {
		t.Fatalf("%v %v %+v", err, next, evs)
	}
	if evs, next, err := tr.Timeline(context.Background(), time.UnixMilli(0), *next, nil, 5); err != nil || len(evs) != 0 || next != nil {
		t.Fatalf("%v %v %+v", err, next, evs)
	}
}

type mux struct{ *http.ServeMux }

func (m mux) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(p, h)
}

func TestHandlers(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	state := fullState()
	tr := tracker(t, db, state)
	m := mux{http.NewServeMux()}
	tr.Register(m)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
		return rec
	}
	for target, want := range map[string]int{
		"/api/v1/changes":                        200,
		"/api/v1/changes?source=units,hearth":    200,
		"/api/v1/changes?before=1000&limit=1000": 200,
		"/api/v1/changes?source=units,evil":      400,
		"/api/v1/changes?before=x":               400,
		"/api/v1/changes?before=0":               400,
		"/api/v1/changes?limit=0":                400,
		"/api/v1/changes?limit=1001":             400,
		"/api/v1/changes/snapshots":              200,
		"/api/v1/changes/diff?from=now&to=now":   200,
		"/api/v1/changes/diff?from=1":            400,
		"/api/v1/changes/diff?from=-1&to=now":    400,
		"/api/v1/changes/diff?from=99&to=now":    404,
	} {
		if rec := do("GET", target, ""); rec.Code != want {
			t.Errorf("%s: %d %s", target, rec.Code, rec.Body)
		}
	}
	for body, want := range map[string]int{
		`{"label":""}`:     400,
		`{"label":"   "}`:  400,
		`{"label":"a\nb"}`: 400,
		`{"label":"` + strings.Repeat("x", 81) + `"}`: 400,
		`{"label":"x","extra":1}`:                     400,
		`not json`:                                    400,
	} {
		if rec := do("POST", "/api/v1/changes/snapshots", body); rec.Code != want {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}

	rec := do("POST", "/api/v1/changes/snapshots", `{"label":" before the update "}`)
	var snap store.Snapshot
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &snap) != nil || snap.Label != "before the update" || snap.Auto {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"data"`) {
		t.Errorf("snapshot data sent: %s", rec.Body)
	}

	state[SourceUnits]["system/new.service"] = "enabled"
	rec = do("GET", fmt.Sprintf("/api/v1/changes/diff?from=%d&to=now", snap.ID), "")
	var diff struct {
		Sections []SectionDiff `json:"sections"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &diff) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	found := false
	for _, s := range diff.Sections {
		found = found || (s.Source == SourceUnits && len(s.Added) == 1 && s.Added[0].Key == "system/new.service")
	}
	if !found {
		t.Errorf("%+v", diff)
	}

	if rec := do("DELETE", "/api/v1/changes/snapshots/x", ""); rec.Code != 400 {
		t.Errorf("bad id: %d", rec.Code)
	}
	if rec := do("DELETE", "/api/v1/changes/snapshots/99", ""); rec.Code != 404 {
		t.Errorf("no such id: %d", rec.Code)
	}
	if rec := do("DELETE", fmt.Sprintf("/api/v1/changes/snapshots/%d", snap.ID), ""); rec.Code != 204 {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}

	// Both actions are in the audit log, so on the timeline (S18).
	rows, err := db.AuditBetween(ctx, time.UnixMilli(0), time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, r := range rows {
		actions[r.Action] = r.Result == "ok"
	}
	if !actions["changes.snapshot"] || !actions["changes.delete_snapshot"] {
		t.Errorf("%+v", rows)
	}
}

func TestMarkers(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tr := New(Config{Store: db, LogDir: t.TempDir(), Clock: func() time.Time { return at }})
	for _, c := range []store.Change{
		{Time: at.Add(-time.Hour), Source: SourceContainers, Kind: KindRestarted, Subject: "<b>web</b>"},
		{Time: at.Add(-2 * time.Hour), Source: SourceContainers, Kind: KindAdded, Subject: "not a marker"},
	} {
		if err := db.AddChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.recordBoot(ctx, at.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tr.recordBoot(ctx, at.Add(-3*time.Hour+time.Minute)); err != nil { // the same boot, read again
		t.Fatal(err)
	}
	for _, e := range []store.AuditEntry{
		{Origin: store.OriginLocal, Action: "profile.activate", Target: "Dev", Result: "ok"},
		{Origin: store.OriginLocal, Action: "profile.activate", Target: "failed", Result: "error: no"},
		{Origin: store.OriginLocal, Action: "unit.stop", Target: "not a marker", Result: "ok"},
	} {
		if err := db.Audit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	m, err := tr.Markers(ctx, at.Add(-24*time.Hour), at)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m[0].Kind != MarkContainer || m[0].Subject != "<b>web</b>" || m[1].Kind != MarkBoot {
		t.Fatalf("%+v", m)
	}
	// Audit rows are stamped with the real clock.
	m, err = tr.Markers(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || len(m) != 1 || m[0].Kind != MarkProfile || m[0].Subject != "Dev" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestUpgradeRuns(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	up := func(d time.Duration, s string) Event { return Event{Time: at.Add(d), Kind: KindUpgraded, Subject: s} }
	evs := []Event{
		up(-time.Hour, "old"), up(0, "a"), up(time.Minute, "b"), up(8*time.Minute, "c"),
		{Time: at.Add(9 * time.Minute), Kind: KindInstalled, Subject: "new"},
		up(time.Hour, "d"), up(3*time.Hour, "late"),
	}
	got := upgradeRuns(evs, at.Add(-time.Minute), at.Add(2*time.Hour))
	if len(got) != 2 || got[0].Count != 3 || got[0].Subject != "" || got[1].Count != 1 || got[1].Subject != "d" {
		t.Fatalf("%+v", got)
	}
}

func TestParseBootTime(t *testing.T) {
	if got, ok := parseBootTime([]byte("cpu 1 2\nbtime 1790000000\nprocesses 3\n")); !ok || got.Unix() != 1790000000 {
		t.Fatalf("%v %v", got, ok)
	}
	for _, s := range []string{"", "btime\n", "btime x\n", "btime 0\n", "btime -5\n"} {
		if _, ok := parseBootTime([]byte(s)); ok {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestMarkerRange(t *testing.T) {
	good := url.Values{"from": {"1000"}, "to": {"2000"}}
	if from, to, err := parseMarkerRange(good); err != nil || from.UnixMilli() != 1000 || to.UnixMilli() != 2000 {
		t.Fatal(from, to, err)
	}
	for _, q := range []string{"", "from=1", "from=2&to=1", "from=1&to=1", "from=-1&to=5", "from=a&to=b", "from=1&to=99999999999999"} {
		v, _ := url.ParseQuery(q)
		if _, _, err := parseMarkerRange(v); err == nil {
			t.Errorf("accepted %q", q)
		}
	}
}

func FuzzMarkerRange(f *testing.F) {
	f.Add("1000", "2000")
	f.Add("-1", "9223372036854775807")
	f.Fuzz(func(t *testing.T, a, b string) {
		from, to, err := parseMarkerRange(url.Values{"from": {a}, "to": {b}})
		if err == nil && (!from.Before(to) || to.Sub(from) > maxMarkerSpan) {
			t.Fatalf("%q %q", a, b)
		}
	})
}

func FuzzParseBootTime(f *testing.F) {
	f.Add([]byte("btime 1790000000\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if got, ok := parseBootTime(b); ok && got.Unix() <= 0 {
			t.Fatalf("%q -> %v", b, got)
		}
	})
}
