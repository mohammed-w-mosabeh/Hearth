package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/backup"
)

type backupBroker struct {
	fakeBroker
	preview  broker.BackupPreview
	restore  broker.RestorePreview
	list     broker.BackupList
	refuse   string // a broker refusal for the previews
	asked    []string
	lastBody string
}

func (f *backupBroker) Call(ctx context.Context, method string, params, out any) error {
	b, _ := json.Marshal(params)
	f.asked = append(f.asked, method)
	f.lastBody = string(b)
	switch method {
	case "stack.backup.preview", "stack.restore.preview":
		if f.refuse != "" {
			return broker.Errorf(broker.CodeRefused, "%s", f.refuse)
		}
		if method == "stack.backup.preview" {
			return remarshal(f.preview, out)
		}
		return remarshal(f.restore, out)
	case "stack.backup.list":
		return remarshal(f.list, out)
	}
	return f.fakeBroker.Call(ctx, method, params, out)
}

func backupRig(t *testing.T) (*actions.Registry, *backupBroker) {
	t.Helper()
	fb := &backupBroker{
		preview: broker.BackupPreview{Name: "web", Running: true, Estimate: 3 << 20, Free: 10 << 30, Enough: true, Keep: 7,
			Volumes: []broker.BackupVolume{{Name: "web_data", Size: 2 << 20, Included: true}, {Name: "shared", Size: -1, Why: "not a plain local volume"}},
			Binds:   []backup.Bind{{Real: "/srv/site", Size: 100, Included: true}, {Real: "/mnt/disk/media", Size: 9, OtherFS: true}},
			Secrets: []string{"DB_PASSWORD"}, Prune: []string{"20261101T000000Z.tar.zst"}, Archives: []string{}},
		restore: broker.RestorePreview{Name: "web", Archive: "20261106T100000Z.tar.zst", Token: "tok", Running: true,
			Volumes: []broker.RestoreVolume{{Name: "web_data", Archived: 2048, Current: 4096, Exists: true}},
			Secrets: []string{"DB_PASSWORD"}, Binds: []backup.Bind{{Real: "/srv/site", Included: true}}, Flags: []string{}},
	}
	reg := actions.NewRegistry(nil)
	Register(reg, fb, fakeTags{}, nil, nil)
	return reg, fb
}

func TestBackupAction(t *testing.T) {
	reg, fb := backupRig(t)
	r := actions.Request{Target: "web", Params: map[string]string{"keep": "3"}}
	p := plan(t, reg, "backup", r)
	if len(p.Blocked) != 0 || p.Confirm != "" || !strings.Contains(p.Summary, "3.0 MiB before compression") {
		t.Fatalf("%+v", p)
	}
	text := ""
	for _, it := range p.Items {
		text += it.Name + ": " + it.Detail + "\n"
	}
	for _, want := range []string{"web_data: volume, 2.0 MiB", "shared: volume, left out", "/mnt/disk/media: bind folder on another filesystem, left out",
		"secrets.env: the app's secrets, by name: DB_PASSWORD", "20261101T000000Z.tar.zst: an older backup, removed to keep 3", "stopped during the backup"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if res := run(t, reg, "backup", r, p); len(res.Failed) != 0 {
		t.Fatalf("%+v", res)
	}
	var sent broker.StackBackupParams
	if err := json.Unmarshal([]byte(fb.lastBody), &sent); err != nil || fb.asked[len(fb.asked)-1] != "stack.backup" {
		t.Fatalf("sent %s to %v", fb.lastBody, fb.asked)
	}
	if !sent.StopFirst || sent.Keep != 3 || len(sent.Include) != 0 {
		t.Fatalf("params %+v", sent)
	}
	// Including the disk asks for exactly it.
	fb.preview.Binds[1].Included = true
	r = actions.Request{Target: "web", Params: map[string]string{"stop": "false", "include": `["/mnt/disk/media"]`}}
	p = plan(t, reg, "backup", r)
	run(t, reg, "backup", r, p)
	_ = json.Unmarshal([]byte(fb.lastBody), &sent)
	if sent.StopFirst || len(sent.Include) != 1 || sent.Include[0] != "/mnt/disk/media" {
		t.Fatalf("params %+v", sent)
	}
	// Not enough space: blocked, nothing runs.
	fb.preview.Enough = false
	if p := plan(t, reg, "backup", r); len(p.Blocked) != 1 || p.State != nil {
		t.Fatalf("%+v", p)
	}
	a, _ := reg.Lookup(Kind + "/backup")
	for _, bad := range []map[string]string{{"keep": "0"}, {"keep": "31"}, {"stop": "yes"}, {"include": "/etc"}, {"x": "1"}} {
		if a.Validate(actions.Request{Target: "web", Params: bad}) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if a.Validate(actions.Request{Target: "../web", Params: map[string]string{}}) == nil {
		t.Error("accepted a bad name")
	}
}

func TestRestoreAction(t *testing.T) {
	reg, fb := backupRig(t)
	r := actions.Request{Target: "web", Params: map[string]string{"archive": "20261106T100000Z.tar.zst"}}
	p := plan(t, reg, "restore", r)
	if p.Confirm != "web" || len(p.Blocked) != 0 {
		t.Fatalf("%+v", p)
	}
	text := ""
	for _, it := range p.Items {
		text += it.Name + ": " + it.Detail + "\n"
	}
	for _, want := range []string{"web_data: volume, overwritten: 4.0 KiB now, 2.0 KiB in the backup", "secrets.env: replaced by the backup's: DB_PASSWORD",
		"/srv/site: bind folder, not restored", "stopped, then started"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	run(t, reg, "restore", r, p)
	var sent broker.StackRestoreParams
	if err := json.Unmarshal([]byte(fb.lastBody), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Token != "tok" || sent.TypedName != "web" || sent.Archive != "20261106T100000Z.tar.zst" || sent.AckFlags == nil {
		t.Fatalf("params %+v", sent)
	}
	fb.refuse = "the archive is not the size its manifest records"
	if p := plan(t, reg, "restore", r); len(p.Blocked) != 1 || p.State != nil || !strings.Contains(p.Blocked[0].Detail, "size") {
		t.Fatalf("%+v", p)
	}
	a, _ := reg.Lookup(Kind + "/restore")
	for _, bad := range []string{"", "../x.tar.zst", "20261106T100000Z.tar.gz"} {
		if a.Validate(actions.Request{Target: "web", Params: map[string]string{"archive": bad}}) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestBackupRoutes(t *testing.T) {
	_, fb := backupRig(t)
	fb.list = broker.BackupList{Name: "web", Dir: backup.StackDir("web"), Archives: []broker.BackupEntry{{Archive: "20261106T100000Z.tar.zst"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/app-backups/{name}", HandleBackups(fb, nil))
	mux.HandleFunc("GET /api/v1/app-backups/{name}/preview", HandleBackupPreview(fb))
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	if w := get("/api/v1/app-backups/web"); w.Code != 200 || !strings.Contains(w.Body.String(), "20261106T100000Z.tar.zst") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := get("/api/v1/app-backups/web/preview?keep=2&include=/mnt/disk/media"); w.Code != 200 || !strings.Contains(fb.lastBody, `"include":["/mnt/disk/media"]`) || !strings.Contains(fb.lastBody, `"keep":2`) {
		t.Fatalf("%d %s, sent %s", w.Code, w.Body, fb.lastBody)
	}
	for _, bad := range []string{"/api/v1/app-backups/W", "/api/v1/app-backups/web/preview?keep=99"} {
		if w := get(bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	fb.refuse = "refused: a bind moved"
	if w := get("/api/v1/app-backups/web/preview"); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d", w.Code)
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 3 << 20: "3.0 MiB", 5 << 40: "5.0 TiB"} {
		if got := Size(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
}

func fakeAnalyze(_ context.Context, name string, args ...string) (safeexec.Result, error) {
	if name != "calendar-check" || args[len(args)-1] == "never" {
		return safeexec.Result{Stderr: []byte("Failed to parse calendar specification 'never'")}, errors.New("exit 1")
	}
	return safeexec.Result{Stdout: []byte("  Original form: daily\nNormalized form: *-*-* 00:00:00\n    Next elapse: Sat 2026-11-07 00:00:00 UTC\n       (in UTC): Sat 2026-11-07 00:00:00 UTC\n")}, nil
}

func TestScheduleAction(t *testing.T) {
	_, fb := backupRig(t)
	reg := actions.NewRegistry(nil)
	RegisterSchedule(reg, fb, fakeAnalyze)
	r := actions.Request{Target: "web", Params: map[string]string{"on_calendar": "daily", "keep": "5", "stop": "true"}}
	a, _ := reg.Lookup(Kind + "/schedule")
	if err := a.Validate(r); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan(context.Background(), r)
	if err != nil || len(p.Blocked) != 0 || !strings.Contains(p.Summary, "*-*-* 00:00:00, keeping 5; the app is stopped") {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := a.Run(context.Background(), r, p); err != nil {
		t.Fatal(err)
	}
	var sent broker.StackScheduleParams
	if err := json.Unmarshal([]byte(fb.lastBody), &sent); err != nil || fb.asked[len(fb.asked)-1] != "stack.backup.schedule" {
		t.Fatalf("sent %s", fb.lastBody)
	}
	if sent.OnCalendar != "daily" || sent.Keep != 5 || !sent.StopFirst {
		t.Fatalf("params %+v", sent)
	}
	r.Params["on_calendar"] = "never"
	if p, _ := a.Plan(context.Background(), r); len(p.Blocked) != 1 || p.State != nil || !strings.Contains(p.Blocked[0].Detail, "Failed to parse") {
		t.Fatalf("%+v", p)
	}
	for _, bad := range []string{"daily\nExecStart=/bin/sh", " daily", "daily;rm"} {
		if a.Validate(actions.Request{Target: "web", Params: map[string]string{"on_calendar": bad}}) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	off := actions.Request{Target: "web", Params: map[string]string{"on_calendar": ""}}
	if p, err := a.Plan(context.Background(), off); err != nil || p.State.(scheduleState).Params.OnCalendar != "" {
		t.Fatalf("off: %+v %v", p, err)
	}
}

func TestBackupsNextRuns(t *testing.T) {
	_, fb := backupRig(t)
	fb.list = broker.BackupList{Name: "web", Archives: []broker.BackupEntry{}, Schedule: &broker.BackupSchedule{OnCalendar: "daily", Keep: 7}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/app-backups/web", nil)
	r.SetPathValue("name", "web")
	HandleBackups(fb, fakeAnalyze)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"next":[1794009600000]`) || !strings.Contains(w.Body.String(), `"on_calendar":"daily"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
