package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/backup"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Scheduled backups (ROADMAP 9.11, SECURITY S12d). stack.backup.schedule
// saves the settings as backup.json in the stack's folder and writes
// hearth-backup-<stack>.timer, whose only line of substance is a checked
// OnCalendar: it starts the fixed hearth-backup-scheduled@<stack>.service,
// which runs this binary's ScheduledBackup, the same steps as stack.backup,
// with the saved settings. No script is written, so S13's copy does not
// apply; the owner approved the schedule, stop included, once with polkit.
const (
	FileBackup = "backup.json"

	ScheduledUnitPrefix = "hearth-backup-scheduled@"
	// SystemUnitDir is where the timers go (the broker may write there).
	SystemUnitDir = "/etc/systemd/system"
	// lastFile records the last backup's outcome in the backup folder.
	lastFile = ".last.json"
)

// BackupTimer is the timer of name's schedule; ScheduledUnit the fixed
// unit it starts.
func BackupTimer(name string) string   { return "hearth-backup-" + name + ".timer" }
func ScheduledUnit(name string) string { return ScheduledUnitPrefix + name + ".service" }

// BackupSchedule is a stack's saved schedule.
type BackupSchedule struct {
	OnCalendar string   `json:"on_calendar"`
	Keep       int      `json:"keep"`
	StopFirst  bool     `json:"stop_first"`
	Include    []string `json:"include"`
}

// StackScheduleParams is stack.backup.schedule's params: an empty
// OnCalendar turns the schedule off.
type StackScheduleParams struct {
	Name       string   `json:"name"`
	OnCalendar string   `json:"on_calendar"`
	Keep       int      `json:"keep"`
	StopFirst  bool     `json:"stop_first"`
	Include    []string `json:"include"`
}

// BackupLast is how the last backup ended.
type BackupLast struct {
	At        string `json:"at"`
	Scheduled bool   `json:"scheduled"`
	OK        bool   `json:"ok"`
	Archive   string `json:"archive,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *stacks) unitDir() string {
	if s.c.UnitDir == "" {
		return SystemUnitDir
	}
	return s.c.UnitDir
}

// schedule reads a stack's saved schedule; nil when it has none.
func (s *stacks) schedule(name string) *BackupSchedule {
	b, err := s.c.Store.Read(name, FileBackup)
	if err != nil {
		return nil
	}
	var v BackupSchedule
	if json.Unmarshal(b, &v) != nil || v.OnCalendar == "" {
		return nil
	}
	if v.Include == nil {
		v.Include = []string{}
	}
	return &v
}

// last reads the last backup's outcome; nil when there was none.
func (s *stacks) last(name string) *BackupLast {
	b, err := os.ReadFile(filepath.Join(s.backupsDir(), name, lastFile)) // #nosec G304 -- a fixed name under a checked stack's folder
	if err != nil {
		return nil
	}
	var v BackupLast
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	return &v
}

func (s *stacks) recordLast(name string, scheduled bool, res BackupResult, err error) {
	l := BackupLast{At: s.c.Now().UTC().Format(time.RFC3339), Scheduled: scheduled, OK: err == nil, Archive: res.Archive}
	if err != nil {
		l.Error = err.Error()
		var be *Error
		if errors.As(err, &be) {
			l.Error = be.Message
		}
	}
	b, jerr := json.Marshal(l)
	if jerr != nil {
		return
	}
	dir, derr := s.backupFolder(name)
	if derr != nil {
		return
	}
	defer func() { _ = dir.Close() }()
	_ = writeAtomic(dir, lastFile, b)
}

func decodeSchedule(p json.RawMessage) (StackScheduleParams, error) {
	var v StackScheduleParams
	if err := decodeParams(p, &v); err != nil {
		return v, err
	}
	if !spec.ValidName(v.Name) {
		return v, badParam("not a stack name")
	}
	if v.OnCalendar == "" {
		return v, nil
	}
	if v.Keep == 0 {
		v.Keep = backup.KeepDefault
	}
	if v.Keep < backup.KeepMin || v.Keep > backup.KeepMax {
		return v, badParam("keep is 1 to 30")
	}
	if err := safeexec.CalendarExpr(v.OnCalendar); err != nil {
		return v, badParam("not a calendar expression")
	}
	if v.Include == nil {
		v.Include = []string{}
	}
	return v, nil
}

// checkSchedule checks the stack, the calendar's meaning (systemd-analyze)
// and the binds to include, writing nothing.
func (s *stacks) checkSchedule(ctx context.Context, p json.RawMessage) (StackScheduleParams, error) {
	v, err := decodeSchedule(p)
	if err != nil {
		return v, err
	}
	if _, _, _, err := s.stored(v.Name); err != nil {
		return v, err
	}
	if v.OnCalendar == "" {
		return v, nil
	}
	if _, err := s.c.Analyze(ctx, "calendar-check", "calendar", "--iterations=5", "--", v.OnCalendar); err != nil {
		return v, badParam("systemd does not understand the schedule %q", v.OnCalendar)
	}
	if _, err := s.planBackup(ctx, StackBackupParams{Name: v.Name, Keep: v.Keep, Include: v.Include}); err != nil {
		return v, err
	}
	return v, nil
}

func (s *stacks) scheduleMethod() Method {
	return Method{Name: "stack.backup.schedule", Privileged: true, Action: ActionStackBackup, Timeout: 2 * time.Minute,
		Check: func(ctx context.Context, p json.RawMessage) error {
			_, err := s.checkSchedule(ctx, p)
			return pre(err)
		},
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			v, err := decodeSchedule(p)
			if err != nil {
				return nil, err
			}
			defer s.locks.lock(v.Name)()
			if v, err = s.checkSchedule(ctx, p); err != nil {
				return nil, err
			}
			if v.OnCalendar == "" {
				if err := s.unschedule(ctx, v.Name); err != nil {
					return nil, err
				}
				return map[string]string{"schedule": ""}, nil
			}
			b, err := json.Marshal(BackupSchedule{OnCalendar: v.OnCalendar, Keep: v.Keep, StopFirst: v.StopFirst, Include: v.Include})
			if err != nil {
				return nil, err
			}
			if err := s.c.Store.Write(v.Name, map[string][]byte{FileBackup: b}); err != nil {
				return nil, Errorf(CodeFailed, "the schedule could not be saved")
			}
			if err := s.writeTimer(ctx, v.Name, v.OnCalendar); err != nil {
				return nil, err
			}
			return map[string]string{"schedule": v.OnCalendar}, nil
		}}
}

func renderBackupTimer(name, calendar string) string {
	return "# Written by hearth-broker when a backup schedule is set (SECURITY S12d).\n" +
		"[Unit]\nDescription=Hearth: back up the app " + name + " on its schedule\n\n" +
		"[Timer]\nOnCalendar=" + calendar + "\nPersistent=true\nRandomizedDelaySec=10min\n" +
		"Unit=" + ScheduledUnit(name) + "\n\n" +
		"[Install]\nWantedBy=timers.target\n"
}

// writeTimer writes, enables and (re)starts the stack's timer.
func (s *stacks) writeTimer(ctx context.Context, name, calendar string) error {
	if !spec.ValidName(name) || safeexec.CalendarExpr(calendar) != nil {
		return badParam("not a schedule")
	}
	bus, err := s.c.Bus(ctx)
	if err != nil {
		return Errorf(CodeFailed, "the system bus is not available")
	}
	defer func() { _ = bus.Close() }()
	failed := func(step string) error { return Errorf(CodeFailed, "the backup timer could not be %s", step) }
	timer := BackupTimer(name)
	path := filepath.Join(s.unitDir(), timer)
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	// O_EXCL: a link left in the way is refused, not followed.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a unit file, world-readable like all of them; a checked name
	if err != nil {
		return failed("written")
	}
	_, werr := f.WriteString(renderBackupTimer(name, calendar))
	if cerr := f.Close(); werr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return failed("written")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return failed("written")
	}
	var job string
	var carries bool
	var changes [][]any
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".Reload", nil); err != nil {
		return failed("reloaded")
	}
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".EnableUnitFiles", []any{&carries, &changes}, []string{timer}, false, true); err != nil {
		return failed("enabled")
	}
	// Restart, not Start: a timer that was running must take the new time.
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".RestartUnit", []any{&job}, timer, "replace"); err != nil {
		return failed("started")
	}
	return nil
}

// unschedule stops and removes the stack's timer and its settings.
func (s *stacks) unschedule(ctx context.Context, name string) error {
	timer := BackupTimer(name)
	path := filepath.Join(s.unitDir(), timer)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		_ = s.removeSchedule(name)
		return nil
	}
	bus, err := s.c.Bus(ctx)
	if err != nil {
		return Errorf(CodeFailed, "the system bus is not available")
	}
	defer func() { _ = bus.Close() }()
	var job string
	var changes [][]any
	// Not loaded, or already stopped: either way it no longer runs.
	_ = bus.Call(ctx, sdDest, sdPath, sdManager+".StopUnit", []any{&job}, timer, "replace")
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".DisableUnitFiles", []any{&changes}, []string{timer}, false); err != nil {
		return Errorf(CodeFailed, "the backup timer could not be disabled")
	}
	if err := os.Remove(path); err != nil {
		return Errorf(CodeFailed, "the backup timer could not be removed")
	}
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".Reload", nil); err != nil {
		return Errorf(CodeFailed, "systemd could not be reloaded")
	}
	return s.removeSchedule(name)
}

func (s *stacks) removeSchedule(name string) error {
	r, err := s.c.Store.open(name, false)
	if err != nil {
		return nil // no stack, no settings
	}
	defer func() { _ = r.Close() }()
	if err := r.Remove(FileBackup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Errorf(CodeFailed, "the schedule could not be removed")
	}
	return nil
}

// ScheduledBackup is hearth-backup-scheduled@<name>: one backup with the
// saved settings, the stack's lock held as the broker holds it. Binds
// chosen then that are gone now are left out, not refused, so one moved
// disk does not stop every backup.
func ScheduledBackup(ctx context.Context, c StackConfig, name string) error {
	s := newStacks(c)
	if !spec.ValidName(name) {
		return errors.New("not a stack name")
	}
	defer s.locks.lock(name)()
	sched := s.schedule(name)
	if sched == nil {
		return errors.New("the app has no backup schedule")
	}
	v := StackBackupParams{Name: name, StopFirst: sched.StopFirst, Keep: sched.Keep, Include: []string{}}
	if len(sched.Include) > 0 {
		if pl, err := s.planBackup(ctx, StackBackupParams{Name: name, Keep: sched.Keep}); err == nil {
			for _, b := range pl.preview.Binds {
				for _, want := range sched.Include {
					if b.OtherFS && b.Real == want {
						v.Include = append(v.Include, want)
					}
				}
			}
		}
	}
	res, err := s.doBackup(ctx, v)
	s.recordLast(name, true, res, err)
	if s.c.Audit != nil {
		now := s.c.Now()
		result := "ok: " + res.Archive
		if err != nil {
			result = "failed: " + err.Error()
		}
		s.c.Audit.Record(AuditRecord{Time: now, TS: now.UnixMilli(), Method: "stack.backup.scheduled", Result: fmt.Sprintf("%s: %s", name, result)})
	}
	return err
}

// flock takes an exclusive lock on the stack's lock file, so the broker
// and a scheduled backup never work on one stack at once. It waits.
func flock(dir, name string) func() {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, "."+name+".lock"), os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o600) // #nosec G304 -- a checked stack name
	if err != nil {
		return func() {}
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil { // #nosec G115 -- a file descriptor
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN) // #nosec G115 -- a file descriptor
		_ = f.Close()
	}
}
