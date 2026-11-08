package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/mohammed-w-mosabeh/hearth/internal/pathcheck"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/backup"
)

const backupSpec = `{"version":1,"name":"web","bind_roots":["/srv/site"],"volumes":{"db":{}},"services":{"app":{"image":"nginx:1.27",
"volumes":[{"type":"volume","source":"db","target":"/data"},{"type":"bind","source":"/srv/site","target":"/usr/share/nginx/html"}]}}}`

type fakeVolumes map[string]VolumeInfo

func (f fakeVolumes) BackupVolumes(_ context.Context, names []string) (map[string]VolumeInfo, error) {
	out := map[string]VolumeInfo{}
	for _, n := range names {
		v, ok := f[n]
		if !ok {
			v = VolumeInfo{Size: -1}
		}
		out[n] = v
	}
	return out, nil
}

type backupRig struct {
	*lifeRig
	cfg      StackConfig
	bus      *recBus
	units    string
	audited  []AuditRecord
	dir      string
	free     uint64
	vols     fakeVolumes
	plans    []backup.Plan
	restores []backup.RestorePlan
	unitErr  error
	changed  bool
	clock    time.Time
}

func newBackupRig(t *testing.T) *backupRig {
	t.Helper()
	b := &backupRig{dir: filepath.Join(t.TempDir(), "backups"), free: 1 << 40,
		vols:  fakeVolumes{"web_db": {Exists: true, Plain: true, Size: 1000}},
		clock: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)}
	measure := func(context.Context, json.RawMessage) (pathcheck.Reply, error) {
		r := okPaths()
		disk := r.Binds[0]
		disk.Source, disk.Real, disk.Dev, disk.Target = "/mnt/disk/media", "/mnt/disk/media", "8:17", "/media"
		r.Binds = append(r.Binds, disk)
		r.BackupDev = "8:1"
		r.Sizes = map[string]pathcheck.Size{"/srv/site": {Bytes: 100}, "/mnt/disk/media": {Bytes: 5000, Partial: true}}
		return r, nil
	}
	b.bus = &recBus{}
	b.units = t.TempDir()
	b.lifeRig = newLifeRigWith(t, func(_ *lifeRig, c *StackConfig) {
		c.Measure, c.Volumes, c.Backups = measure, b.vols, b.dir
		c.FreeSpace = func(string) (uint64, error) { return b.free, nil }
		c.Now = func() time.Time { return b.clock }
		c.UnitDir = b.units
		c.Bus = func(context.Context) (SystemBus, error) { return b.bus, nil }
		c.Analyze = func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
			if name != "calendar-check" || args[len(args)-1] == "never" {
				return safeexec.Result{ExitCode: 1}, errors.New("exit 1")
			}
			return safeexec.Result{}, nil
		}
		c.Audit = auditFunc(func(r AuditRecord) { b.audited = append(b.audited, r) })
		b.cfg = *c
	})
	b.unit = func(unit string) error {
		if b.unitErr != nil {
			return b.unitErr
		}
		switch {
		case strings.HasPrefix(unit, BackupUnitPrefix):
			var p backup.Plan
			if err := readJSON(filepath.Join(b.dir, "web", backup.PlanFile), &p); err != nil {
				return err
			}
			b.plans = append(b.plans, p)
			if b.changed {
				if err := os.WriteFile(filepath.Join(b.dir, "web", backup.ChangedName(p.Output)), nil, 0o600); err != nil {
					return err
				}
			}
			return os.WriteFile(filepath.Join(b.dir, "web", p.Output), []byte("archive "+p.Output), 0o600)
		case strings.HasPrefix(unit, RestoreUnitPrefix):
			var p backup.RestorePlan
			if err := readJSON(filepath.Join(b.dir, "web", backup.RestoreFile), &p); err != nil {
				return err
			}
			b.restores = append(b.restores, p)
			return nil
		}
		return errors.New("no such unit " + unit)
	}
	if _, err := b.call(t, "stack.deploy", deployParams(backupSpec)); err != nil {
		t.Fatal(err)
	}
	b.ran = nil
	return b
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- test files
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func TestStackBackupPreview(t *testing.T) {
	b := newBackupRig(t)
	got, err := b.call(t, "stack.backup.preview", map[string]any{"name": "web"})
	if err != nil {
		t.Fatal(err)
	}
	pv := got.(BackupPreview)
	if len(pv.Volumes) != 1 || !pv.Volumes[0].Included || pv.Volumes[0].Name != "web_db" {
		t.Fatalf("volumes %+v", pv.Volumes)
	}
	if len(pv.Binds) != 2 || !pv.Binds[1].Included || pv.Binds[0].Included || !pv.Binds[0].OtherFS {
		t.Fatalf("binds %+v", pv.Binds)
	}
	if pv.Estimate != 1<<20+1000+100 || !pv.Enough || pv.Partial || pv.Keep != backup.KeepDefault || !pv.Running {
		t.Fatalf("preview %+v", pv)
	}
	got, err = b.call(t, "stack.backup.preview", map[string]any{"name": "web", "include": []string{"/mnt/disk/media"}})
	if err != nil {
		t.Fatal(err)
	}
	if pv := got.(BackupPreview); pv.Estimate != 1<<20+1000+100+5000 || !pv.Partial || !pv.Binds[0].Included {
		t.Fatalf("with the disk %+v", pv)
	}
	for name, params := range map[string]map[string]any{
		"not a bind": {"name": "web", "include": []string{"/etc"}},
		"keep":       {"name": "web", "keep": 31},
		"no stack":   {"name": "db"},
		"bad name":   {"name": "../web"},
		"unknown":    {"name": "web", "x": 1},
	} {
		if _, err := b.call(t, "stack.backup.preview", params); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	b.vols["web_db"] = VolumeInfo{Exists: true, Plain: false, Size: 9}
	got, _ = b.call(t, "stack.backup.preview", map[string]any{"name": "web"})
	if pv := got.(BackupPreview); pv.Volumes[0].Included || pv.Volumes[0].Why == "" {
		t.Fatalf("a volume with options backed up: %+v", pv.Volumes)
	}
}

func TestStackBackup(t *testing.T) {
	b := newBackupRig(t)
	m := b.methods["stack.backup"]
	if !m.Privileged || m.Action != ActionStackBackup {
		t.Fatal("backup is not behind its polkit action")
	}
	got, err := b.call(t, "stack.backup", map[string]any{"name": "web", "stop_first": true, "keep": 2})
	if err != nil {
		t.Fatal(err)
	}
	res := got.(BackupResult)
	if res.Archive != "20261106T100000Z.tar.zst" || res.Warning != "" || len(res.Removed) != 0 {
		t.Fatalf("result %+v", res)
	}
	if !slices.Equal(b.ran, []string{"stop", "start"}) {
		t.Fatalf("ran %v", b.ran)
	}
	want := []string{backup.StackFolder("web"), backup.VolumeData("web_db"), "/srv/site"}
	if len(b.plans) != 1 || !slices.Equal(b.plans[0].Paths, want) {
		t.Fatalf("plans %+v", b.plans)
	}
	folder := filepath.Join(b.dir, "web")
	for _, f := range []string{res.Archive, backup.ManifestOf(res.Archive)} {
		fi, err := os.Stat(filepath.Join(folder, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, err, fi)
		}
	}
	for _, gone := range []string{backup.PlanFile, backup.PartialName("20261106T100000Z")} {
		if _, err := os.Stat(filepath.Join(folder, gone)); err == nil {
			t.Fatalf("%s left behind", gone)
		}
	}
	if fi, _ := os.Stat(folder); fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v", fi.Mode())
	}
	var man backup.Manifest
	if err := readJSON(filepath.Join(folder, backup.ManifestOf(res.Archive)), &man); err != nil {
		t.Fatal(err)
	}
	sum, size, _ := backup.FileSHA256(backup.Roots{Backups: b.dir}, "web", res.Archive)
	if man.SHA256 != sum || man.Size != size || !man.StoppedFirst || man.Keep != 2 || len(man.Spec) == 0 {
		t.Fatalf("manifest %+v", man)
	}

	// Live, with a changed file, twice more: the oldest goes.
	b.changed = true
	b.ran = nil
	for i := 1; i <= 2; i++ {
		b.clock = b.clock.Add(time.Hour)
		got, err = b.call(t, "stack.backup", map[string]any{"name": "web", "keep": 2})
		if err != nil {
			t.Fatal(err)
		}
	}
	res = got.(BackupResult)
	if res.Warning == "" || !slices.Equal(res.Removed, []string{"20261106T100000Z.tar.zst"}) || len(b.ran) != 0 {
		t.Fatalf("result %+v, ran %v", res, b.ran)
	}
	if _, err := os.Stat(filepath.Join(folder, "20261106T100000Z.json")); err == nil {
		t.Fatal("the pruned archive's manifest stayed")
	}
	list, err := b.call(t, "stack.backup.list", map[string]any{"name": "web"})
	if err != nil {
		t.Fatal(err)
	}
	if l := list.(BackupList); len(l.Archives) != 2 || l.Archives[0].Archive != "20261106T120000Z.tar.zst" || l.Archives[0].Warning == "" {
		t.Fatalf("list %+v", l)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "nginx") {
		t.Fatal("the list holds the spec")
	}
	// Same second: refused, nothing overwritten.
	if _, err := b.call(t, "stack.backup", map[string]any{"name": "web", "keep": 2}); errCode(err) != CodeConflict {
		t.Fatalf("same second: %v", err)
	}
}

func TestStackBackupRefusedBeforePolkit(t *testing.T) {
	b := newBackupRig(t)
	m := b.methods["stack.backup"]
	b.free = 1 << 20
	for name, params := range map[string]map[string]any{
		"space":    {"name": "web"},
		"keep":     {"name": "web", "keep": 0.5},
		"no stack": {"name": "db"},
		"not bind": {"name": "web", "include": []string{"/home"}},
	} {
		p, _ := json.Marshal(params)
		if err := m.Check(context.Background(), p); errCode(err) != CodeBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(b.plans) != 0 || len(b.ran) != 0 {
		t.Fatal("something ran")
	}
}

func TestStackBackupUnitFails(t *testing.T) {
	b := newBackupRig(t)
	b.unitErr = errors.New("tar failed")
	if _, err := b.call(t, "stack.backup", map[string]any{"name": "web", "stop_first": true}); err == nil {
		t.Fatal("no error")
	}
	if !slices.Equal(b.ran, []string{"stop", "start"}) {
		t.Fatalf("the app was not started again: %v", b.ran)
	}
	entries, _ := os.ReadDir(filepath.Join(b.dir, "web"))
	if len(entries) != 1 || entries[0].Name() != ".last.json" {
		t.Fatalf("left %v", entries)
	}
	list, _ := b.call(t, "stack.backup.list", map[string]any{"name": "web"})
	if l := list.(BackupList).Last; l == nil || l.OK || l.Scheduled || !strings.Contains(l.Error, "the backup failed") {
		t.Fatalf("last %+v", l)
	}
}

func (b *backupRig) backupOnce(t *testing.T) string {
	t.Helper()
	got, err := b.call(t, "stack.backup", map[string]any{"name": "web"})
	if err != nil {
		t.Fatal(err)
	}
	b.ran = nil
	return got.(BackupResult).Archive
}

func (b *backupRig) restorePreview(t *testing.T, archive string) RestorePreview {
	t.Helper()
	got, err := b.call(t, "stack.restore.preview", map[string]any{"name": "web", "archive": archive})
	if err != nil {
		t.Fatal(err)
	}
	return got.(RestorePreview)
}

func TestStackRestore(t *testing.T) {
	b := newBackupRig(t)
	archive := b.backupOnce(t)
	pv := b.restorePreview(t, archive)
	if pv.Token == "" || pv.NewStack || pv.SpecChanged || len(pv.Volumes) != 1 || pv.Volumes[0].Archived != 1000 || len(pv.Binds) != 1 {
		t.Fatalf("preview %+v", pv)
	}
	ok := map[string]any{"name": "web", "archive": archive, "token": pv.Token, "typed_name": "web", "ack_flags": []string{}}
	m := b.methods["stack.restore"]
	if !m.Privileged || m.Action != ActionStackRestore {
		t.Fatal("restore is not behind its polkit action")
	}
	if a, d := m.Authorize(mustJSON(t, ok)); a != ActionStackRestore || !strings.Contains(d["polkit.message"], "overwriting") {
		t.Fatalf("authorize %s %v", a, d)
	}
	for name, change := range map[string]func(map[string]any){
		"no typed name":  func(p map[string]any) { delete(p, "typed_name") },
		"wrong name":     func(p map[string]any) { p["typed_name"] = "Web" },
		"no token":       func(p map[string]any) { delete(p, "token") },
		"other token":    func(p map[string]any) { p["token"] = strings.Repeat("0", 32) },
		"other archive":  func(p map[string]any) { p["archive"] = "20261106T100001Z.tar.zst" },
		"flags":          func(p map[string]any) { p["ack_flags"] = []string{"privileged"} },
		"unknown params": func(p map[string]any) { p["x"] = 1 },
	} {
		p := map[string]any{}
		for k, v := range ok {
			p[k] = v
		}
		change(p)
		if err := m.Check(context.Background(), mustJSON(t, p)); errCode(err) != CodeBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(b.restores) != 0 || len(b.ran) != 0 {
		t.Fatal("something ran")
	}
	if _, err := b.call(t, "stack.restore", ok); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.ran, []string{"stop", "create", "up"}) {
		t.Fatalf("ran %v", b.ran)
	}
	man := backup.RestorePlan{}
	if len(b.restores) == 1 {
		man = b.restores[0]
	}
	if !slices.Equal(man.Volumes, []string{"web_db"}) || !man.Secrets || man.Archive != archive || len(man.SHA256) != 64 {
		t.Fatalf("restore plan %+v", b.restores)
	}
	if _, err := os.Stat(filepath.Join(b.dir, "web", backup.RestoreFile)); err == nil {
		t.Fatal("the restore plan stayed")
	}
	// The token is used once.
	if _, err := b.call(t, "stack.restore", ok); errCode(err) != CodeBadParams {
		t.Fatalf("token reused: %v", err)
	}
}

func TestStackRestoreSwappedArchive(t *testing.T) {
	b := newBackupRig(t)
	archive := b.backupOnce(t)
	pv := b.restorePreview(t, archive)
	if err := os.WriteFile(filepath.Join(b.dir, "web", archive), []byte("another archive, longer than the first"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := map[string]any{"name": "web", "archive": archive, "token": pv.Token, "typed_name": "web", "ack_flags": []string{}}
	if _, err := b.call(t, "stack.restore", p); err == nil || len(b.restores) != 0 || len(b.ran) != 0 {
		t.Fatalf("a swapped archive went ahead: %v, ran %v", err, b.ran)
	}
	// A manifest edited to another archive's name is refused too.
	if _, err := b.call(t, "stack.restore.preview", map[string]any{"name": "web", "archive": "../web/" + archive}); err == nil {
		t.Fatal("a path as archive name")
	}
}

func TestStackRestoreAfterDeployChanged(t *testing.T) {
	b := newBackupRig(t)
	archive := b.backupOnce(t)
	pv := b.restorePreview(t, archive)
	changed := strings.Replace(backupSpec, "nginx:1.27", "nginx:1.28", 1)
	if _, err := b.call(t, "stack.deploy", deployParams(changed)); err != nil {
		t.Fatal(err)
	}
	p := map[string]any{"name": "web", "archive": archive, "token": pv.Token, "typed_name": "web", "ack_flags": []string{}}
	if _, err := b.call(t, "stack.restore", p); errCode(err) != CodeBadParams {
		t.Fatalf("restored over a newer deploy without a new preview: %v", err)
	}
	if pv := b.restorePreview(t, archive); !pv.SpecChanged {
		t.Fatal("the changed spec is not shown")
	}
}

func TestStackRestoreRemovedStack(t *testing.T) {
	b := newBackupRig(t)
	archive := b.backupOnce(t)
	if _, err := b.call(t, "stack.remove", map[string]any{"name": "web"}); err != nil {
		t.Fatal(err)
	}
	b.ran = nil
	b.engine.containers = nil
	pv := b.restorePreview(t, archive)
	if !pv.NewStack || !pv.SpecChanged {
		t.Fatalf("preview %+v", pv)
	}
	p := map[string]any{"name": "web", "archive": archive, "token": pv.Token, "typed_name": "web", "ack_flags": []string{}}
	if _, err := b.call(t, "stack.restore", p); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.ran, []string{"create", "up"}) {
		t.Fatalf("ran %v", b.ran)
	}
	if _, err := b.store.Read("web", FileSpec); err != nil {
		t.Fatal("the spec was not restored")
	}
}

func TestStackRestoreDangerAction(t *testing.T) {
	b := newBackupRig(t)
	archive := b.backupOnce(t)
	// The archived spec asks for a flag: its manifest says so.
	folder := filepath.Join(b.dir, "web")
	var man backup.Manifest
	if err := readJSON(filepath.Join(folder, backup.ManifestOf(archive)), &man); err != nil {
		t.Fatal(err)
	}
	man.Spec = json.RawMessage(strings.Replace(backupSpec, `"image":"nginx:1.27",`, `"image":"nginx:1.27","cap_add":["NET_ADMIN"],`, 1))
	man.Spec = json.RawMessage(strings.Replace(string(man.Spec), `"name":"web",`, `"name":"web","opt_in":["cap:NET_ADMIN"],`, 1))
	raw, _ := json.Marshal(man)
	if err := os.WriteFile(filepath.Join(folder, backup.ManifestOf(archive)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	pv := b.restorePreview(t, archive)
	if !slices.Equal(pv.Flags, []string{"cap:NET_ADMIN"}) {
		t.Fatalf("flags %v", pv.Flags)
	}
	p := map[string]any{"name": "web", "archive": archive, "token": pv.Token, "typed_name": "web", "ack_flags": []string{"cap:NET_ADMIN"}}
	if a, d := b.methods["stack.restore"].Authorize(mustJSON(t, p)); a != ActionStackDanger || d["flags"] != "cap:NET_ADMIN" {
		t.Fatalf("authorize %s %v", a, d)
	}
	p["ack_flags"] = []string{}
	if err := b.methods["stack.restore"].Check(context.Background(), mustJSON(t, p)); errCode(err) != CodeBadParams {
		t.Fatalf("flags not acknowledged: %v", err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type recBus struct{ calls []string }

func (b *recBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, _ []any, args ...any) error {
	b.calls = append(b.calls, strings.TrimPrefix(method, sdManager+".")+strings.TrimSuffix(fmt.Sprintln(args...), "\n"))
	return nil
}

func (b *recBus) GetAll(context.Context, string, dbus.ObjectPath, string) (map[string]dbus.Variant, error) {
	return nil, errors.New("not here")
}

func (b *recBus) Close() error { return nil }

type auditFunc func(AuditRecord)

func (f auditFunc) Record(r AuditRecord) { f(r) }

func TestStackBackupSchedule(t *testing.T) {
	b := newBackupRig(t)
	m := b.methods["stack.backup.schedule"]
	if !m.Privileged || m.Action != ActionStackBackup {
		t.Fatal("schedule is not behind the backup action")
	}
	for name, params := range map[string]map[string]any{
		"injection":   {"name": "web", "on_calendar": "daily\nExecStart=/bin/sh"},
		"unit line":   {"name": "web", "on_calendar": "daily [Service]"},
		"meaningless": {"name": "web", "on_calendar": "never"},
		"keep":        {"name": "web", "on_calendar": "daily", "keep": 40},
		"not a bind":  {"name": "web", "on_calendar": "daily", "include": []string{"/etc"}},
		"no stack":    {"name": "db", "on_calendar": "daily"},
		"bad name":    {"name": "web.timer", "on_calendar": "daily"},
	} {
		if err := m.Check(context.Background(), mustJSON(t, params)); errCode(err) != CodeBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(b.bus.calls) != 0 {
		t.Fatalf("bus used before polkit: %v", b.bus.calls)
	}
	if _, err := b.call(t, "stack.backup.schedule", map[string]any{"name": "web", "on_calendar": "Sun *-*-* 03:00", "keep": 5, "stop_first": true}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(b.units, "hearth-backup-web.timer"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OnCalendar=Sun *-*-* 03:00\n", "Unit=hearth-backup-scheduled@web.service\n", "Persistent=true"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("timer lacks %q:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "Exec") {
		t.Fatal("the timer holds a command")
	}
	want := []string{"Reload", "EnableUnitFiles[hearth-backup-web.timer] false true", "RestartUnithearth-backup-web.timer replace"}
	if !slices.Equal(b.bus.calls, want) {
		t.Fatalf("bus %q", b.bus.calls)
	}
	list, _ := b.call(t, "stack.backup.list", map[string]any{"name": "web"})
	if sc := list.(BackupList).Schedule; sc == nil || sc.OnCalendar != "Sun *-*-* 03:00" || sc.Keep != 5 || !sc.StopFirst {
		t.Fatalf("schedule %+v", sc)
	}
	// Off: the timer and the settings go.
	b.bus.calls = nil
	if _, err := b.call(t, "stack.backup.schedule", map[string]any{"name": "web", "on_calendar": ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.units, "hearth-backup-web.timer")); err == nil {
		t.Fatal("the timer stayed")
	}
	if _, err := b.store.Read("web", FileBackup); err == nil {
		t.Fatal("the settings stayed")
	}
	if len(b.bus.calls) != 3 || !strings.HasPrefix(b.bus.calls[0], "StopUnit") {
		t.Fatalf("bus %q", b.bus.calls)
	}
}

func TestScheduledBackup(t *testing.T) {
	b := newBackupRig(t)
	if err := ScheduledBackup(context.Background(), b.cfg, "web"); err == nil {
		t.Fatal("ran without a schedule")
	}
	if _, err := b.call(t, "stack.backup.schedule", map[string]any{"name": "web", "on_calendar": "daily", "keep": 3, "stop_first": true,
		"include": []string{"/mnt/disk/media"}}); err != nil {
		t.Fatal(err)
	}
	b.ran = nil
	if err := ScheduledBackup(context.Background(), b.cfg, "web"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.ran, []string{"stop", "start"}) || len(b.plans) != 1 || !slices.Contains(b.plans[0].Paths, "/mnt/disk/media") {
		t.Fatalf("ran %v, plans %+v", b.ran, b.plans)
	}
	list, _ := b.call(t, "stack.backup.list", map[string]any{"name": "web"})
	l := list.(BackupList)
	if len(l.Archives) != 1 || l.Last == nil || !l.Last.OK || !l.Last.Scheduled || l.Archives[0].Keep != 3 {
		t.Fatalf("list %+v", l)
	}
	if len(b.audited) == 0 || b.audited[len(b.audited)-1].Method != "stack.backup.scheduled" || !strings.Contains(b.audited[len(b.audited)-1].Result, "web: ok") {
		t.Fatalf("audit %+v", b.audited)
	}
	// Removing the app removes its schedule; the archive stays.
	if _, err := b.call(t, "stack.remove", map[string]any{"name": "web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.units, "hearth-backup-web.timer")); err == nil {
		t.Fatal("the timer outlived the app")
	}
	if _, err := os.Stat(filepath.Join(b.dir, "web", l.Archives[0].Archive)); err != nil {
		t.Fatal("the archive went with the app")
	}
}

func TestStackLockIsAFile(t *testing.T) {
	dir := t.TempDir()
	l := &stackLocks{m: map[string]*sync.Mutex{}, dir: dir}
	unlock := l.lock("web")
	f, err := os.OpenFile(filepath.Join(dir, ".web.lock"), os.O_RDWR, 0) // #nosec G304 -- a test's temporary folder
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		t.Fatal("another holder got the lock")
	}
	unlock()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("not released: %v", err)
	}
}
