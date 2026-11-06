package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/docker"
)

type memSettings map[string]json.RawMessage

func (m memSettings) Setting(_ context.Context, k string) (json.RawMessage, bool, error) {
	v, ok := m[k]
	return v, ok, nil
}

func (m memSettings) SetSetting(_ context.Context, k string, v json.RawMessage) error {
	m[k] = v
	return nil
}

// appsBroker answers stack.status (all apps) and stack.backup.list.
type appsBroker struct {
	apps  []broker.StackInfo
	lists map[string]broker.BackupList
	fail  bool
}

func (f *appsBroker) Call(_ context.Context, method string, params, out any) error {
	if f.fail {
		return errors.New("broker down")
	}
	switch method {
	case "stack.status":
		return remarshal(f.apps, out)
	case "stack.backup.list":
		b, _ := json.Marshal(params)
		var p struct{ Name string }
		_ = json.Unmarshal(b, &p)
		l, ok := f.lists[p.Name]
		if !ok {
			return broker.Errorf(broker.CodeNotFound, "no such stack")
		}
		return remarshal(l, out)
	}
	return errors.New("unexpected " + method)
}

func ctr(name, svc, state, health, image string) broker.StackContainer {
	return broker.StackContainer{Name: name, Service: svc, State: state, Health: health, ImageID: image}
}

func appsRig() *appsBroker {
	return &appsBroker{
		apps: []broker.StackInfo{
			{Name: "wiki", State: "running", SpecSHA256: "aaaa1111bbbb2222", Containers: []broker.StackContainer{
				ctr("wiki-web-1", "web", "running", "healthy", "sha256:1"),
			}},
			{Name: "cloud", State: "partial", SpecSHA256: "cccc3333dddd4444", Containers: []broker.StackContainer{
				ctr("cloud-web-1", "web", "running", "unhealthy", "sha256:2"),
				ctr("cloud-db-1", "db", "exited", "", "sha256:3"),
				ctr("cloud-cron-1", "cron", "restarting", "", "sha256:4"),
			}},
			{Name: "old", State: "stopped", SpecSHA256: "eeee5555ffff6666", Containers: []broker.StackContainer{
				ctr("old-web-1", "web", "exited", "", "sha256:5"),
			}},
		},
		lists: map[string]broker.BackupList{
			"wiki":  {Name: "wiki", Last: &broker.BackupLast{At: "2026-11-06T03:00:00Z", OK: true}, Schedule: &broker.BackupSchedule{}},
			"cloud": {Name: "cloud", Last: &broker.BackupLast{At: "2026-11-06T03:05:00Z", Error: "tar failed"}},
			"old":   {Name: "old"},
		},
	}
}

func TestOverview(t *testing.T) {
	list, err := Overview(context.Background(), appsRig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, a := range list {
		names = append(names, a.Name)
	}
	if !reflect.DeepEqual(names, []string{"cloud", "old", "wiki"}) {
		t.Fatalf("order %v", names)
	}
	cloud, old, wiki := list[0], list[1], list[2]
	// An exited container counts only while the app is meant to run.
	if want := []string{"cloud-cron-1", "cloud-db-1", "cloud-web-1"}; !reflect.DeepEqual(cloud.Unhealthy, want) {
		t.Errorf("cloud unhealthy %v", cloud.Unhealthy)
	}
	if len(old.Unhealthy) != 0 || len(wiki.Unhealthy) != 0 {
		t.Errorf("stopped or healthy app listed: %v %v", old.Unhealthy, wiki.Unhealthy)
	}
	if !cloud.BackupFailed() || wiki.BackupFailed() || old.BackupFailed() || old.Backup != nil {
		t.Errorf("backups: %+v %+v %+v", cloud.Backup, wiki.Backup, old.Backup)
	}
	if !wiki.Scheduled || cloud.Scheduled {
		t.Errorf("schedules: %v %v", wiki.Scheduled, cloud.Scheduled)
	}
	if cloud.Updates == nil || len(cloud.Updates) != 0 {
		t.Errorf("updates must be an empty list, not null: %#v", cloud.Updates)
	}

	// An app whose backups cannot be read is still listed.
	f := appsRig()
	delete(f.lists, "wiki")
	if list, err := Overview(context.Background(), f, nil); err != nil || len(list) != 3 || list[2].Backup != nil {
		t.Errorf("unread backups: %+v %v", list, err)
	}
	if _, err := Overview(context.Background(), &appsBroker{fail: true}, nil); err == nil {
		t.Error("a broker failure is not an error")
	}
}

func TestUpdateNotes(t *testing.T) {
	ctx := context.Background()
	settings := memSettings{}
	n := &UpdateNotes{Settings: settings, Clock: func() time.Time { return time.UnixMilli(5) }}
	app := appsRig().apps[1] // cloud
	n.Note(ctx, app, []string{"web", "db"})
	if got := n.Pending(ctx, app); !reflect.DeepEqual(got, []string{"db", "web"}) {
		t.Fatalf("pending %v", got)
	}
	// Kept across a restart.
	again := &UpdateNotes{Settings: settings}
	if got := again.Pending(ctx, app); !reflect.DeepEqual(got, []string{"db", "web"}) {
		t.Errorf("not kept: %v (%s)", got, settings[SettingUpdates])
	}
	// A new image (the update ran) or a new spec lapses it.
	updated := app
	updated.Containers = append([]broker.StackContainer(nil), app.Containers...)
	updated.Containers[0].ImageID = "sha256:new"
	if got := n.Pending(ctx, updated); got != nil {
		t.Errorf("an update ran, still pending: %v", got)
	}
	redeployed := app
	redeployed.SpecSHA256 = "other"
	if got := n.Pending(ctx, redeployed); got != nil {
		t.Errorf("redeployed, still pending: %v", got)
	}
	// Another app has no note; a preview finding nothing clears it.
	if got := n.Pending(ctx, appsRig().apps[0]); got != nil {
		t.Errorf("wiki pending %v", got)
	}
	n.Note(ctx, app, nil)
	if got := n.Pending(ctx, app); got != nil {
		t.Errorf("cleared, still pending: %v", got)
	}
	// A stored name that is not a stack name is dropped on load.
	bad := memSettings{SettingUpdates: json.RawMessage(`{"../x":{"services":["a"]}}`)}
	if got := (&UpdateNotes{Settings: bad}).Pending(ctx, broker.StackInfo{Name: "../x"}); got != nil {
		t.Errorf("bad name loaded: %v", got)
	}
	// nil notes (tests, or no store) are a no-op.
	var none *UpdateNotes
	none.Note(ctx, app, []string{"web"})
	if none.Pending(ctx, app) != nil {
		t.Error("nil notes pending")
	}
}

func TestUpdatePreviewNotes(t *testing.T) {
	// planUpdate records what it found; a registry that could not be
	// asked keeps the earlier note rather than clearing it.
	ctx := context.Background()
	info := broker.StackInfo{Name: "web", State: "running", SpecSHA256: "s",
		Spec:       json.RawMessage(`{"services":{"web":{"image":"nginx:1"}}}`),
		Containers: []broker.StackContainer{ctr("web-web-1", "web", "running", "", "sha256:img")}}
	n := &UpdateNotes{}
	for _, tc := range []struct {
		tags fakeTags
		want []string
	}{
		{fakeTags{status: docker.UpdateNewer, remote: "sha256:abc"}, []string{"web"}},
		{fakeTags{}, []string{"web"}}, // registry not asked: kept
		{fakeTags{status: docker.UpdateCurrent}, nil},
	} {
		b := &fakeBroker{status: []broker.StackInfo{info}}
		if _, err := planUpdate(ctx, b, tc.tags, n, "web"); err != nil {
			t.Fatal(err)
		}
		if got := n.Pending(ctx, info); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("tags %+v: pending %v, want %v", tc.tags, got, tc.want)
		}
	}
}

func TestWatchMetrics(t *testing.T) {
	got := map[string]map[string]float64{}
	observe := func(_ context.Context, _ time.Time, metric string, v map[string]float64) { got[metric] = v }
	tick(context.Background(), appsRig(), nil, observe)
	want := map[string]map[string]float64{
		MetricUnhealthy:    {"cloud": 1},
		MetricBackupFailed: {"cloud": 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("metrics %v", got)
	}
	// A failed read reports nothing, so incidents stay as they were.
	got = map[string]map[string]float64{}
	tick(context.Background(), &appsBroker{fail: true}, nil, observe)
	if len(got) != 0 {
		t.Errorf("reported on failure: %v", got)
	}
}

func TestSnapshotAndReport(t *testing.T) {
	snap, err := Snapshot(context.Background(), appsRig())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"wiki":         "running, spec aaaa1111bbbb",
		"wiki backup":  "2026-11-06T03:00:00Z ok",
		"cloud":        "partial, spec cccc3333dddd",
		"cloud backup": "2026-11-06T03:05:00Z failed",
		"old":          "stopped, spec eeee5555ffff",
	}
	if !reflect.DeepEqual(snap, want) {
		t.Errorf("snapshot %v", snap)
	}
	rows, err := ReportApps(context.Background(), appsRig(), nil)
	if err != nil || len(rows) != 3 {
		t.Fatalf("report %+v %v", rows, err)
	}
	if r := rows[0]; r.Name != "cloud" || r.Backup != "2026-11-06T03:05:00Z" || r.BackupOK || len(r.Unhealthy) != 3 {
		t.Errorf("cloud row %+v", r)
	}
	if r := rows[1]; r.Backup != "" || r.Scheduled {
		t.Errorf("old row %+v", r)
	}
}
