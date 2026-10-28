package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/godbus/dbus/v5"
)

// UserWriter manages the user's own jobs: units in UnitDir
// (~/.config/systemd/user), scripts in JobsDir
// (~/.local/share/hearth/jobs/<id>/run, mode 0700), through the user's
// systemd manager.
type UserWriter struct {
	UnitDir string
	JobsDir string
	Bus     func(context.Context) (Bus, error)
}

// unitChange is EnableUnitFiles' and DisableUnitFiles' a(sss).
type unitChange struct {
	Type, File, Dest string
}

// ScriptPath is where a user job's script lives.
func (w *UserWriter) ScriptPath(id string) string { return filepath.Join(w.JobsDir, id, "run") }

// Save implements Writer.
func (w *UserWriter) Save(ctx context.Context, j Job, paused bool) error {
	if !ValidID(j.ID) {
		return errors.New("bad job id")
	}
	if err := os.MkdirAll(filepath.Join(w.JobsDir, j.ID), 0o700); err != nil { // #nosec G703 -- id: 8 hex digits (ValidID)
		return fmt.Errorf("scheduler: %w", err)
	}
	if err := os.MkdirAll(w.UnitDir, 0o700); err != nil { // #nosec G703 -- the configured unit directory
		return fmt.Errorf("scheduler: %w", err)
	}
	base := filepath.Join(w.UnitDir, UnitBase(j.ID))
	files := []struct {
		path, data string
		mode       os.FileMode
	}{
		{w.ScriptPath(j.ID), j.Script, 0o700},
		{base + ".service", ServiceFile(j, w.ScriptPath(j.ID)), 0o600},
		{base + ".timer", TimerFile(j), 0o600},
	}
	for _, f := range files {
		if err := writeAtomic(f.path, f.data, f.mode); err != nil {
			return err
		}
	}
	return w.manage(ctx, func(bus Bus) error {
		if err := bus.Call(ctx, sdDest, sdPath, sdManager+".Reload", nil); err != nil {
			return err
		}
		if paused {
			return nil
		}
		return startTimer(ctx, bus, j.ID)
	})
}

// startTimer enables the timer at login and (re)starts it so a changed
// schedule applies now.
func startTimer(ctx context.Context, bus Bus, id string) error {
	timer := UnitBase(id) + ".timer"
	var carries bool
	var changes []unitChange
	if err := bus.Call(ctx, sdDest, sdPath, sdManager+".EnableUnitFiles", []any{&carries, &changes}, []string{timer}, false, true); err != nil {
		return err
	}
	var job dbus.ObjectPath
	return bus.Call(ctx, sdDest, sdPath, sdManager+".RestartUnit", []any{&job}, timer, "replace")
}

// stopTimer stops and disables a timer.
func stopTimer(ctx context.Context, bus Bus, id string) error {
	timer := UnitBase(id) + ".timer"
	var job dbus.ObjectPath
	_ = bus.Call(ctx, sdDest, sdPath, sdManager+".StopUnit", []any{&job}, timer, "replace")
	var changes []unitChange
	return bus.Call(ctx, sdDest, sdPath, sdManager+".DisableUnitFiles", []any{&changes}, []string{timer}, false)
}

// Delete implements Writer: the timer and any run are stopped, then the
// units and the script removed.
func (w *UserWriter) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return errors.New("bad job id")
	}
	err := w.manage(ctx, func(bus Bus) error {
		_ = stopTimer(ctx, bus, id)
		var job dbus.ObjectPath
		_ = bus.Call(ctx, sdDest, sdPath, sdManager+".StopUnit", []any{&job}, UnitBase(id)+".service", "replace")
		return nil
	})
	if err != nil {
		return err
	}
	base := filepath.Join(w.UnitDir, UnitBase(id))
	for _, p := range []string{base + ".timer", base + ".service"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("scheduler: %w", err)
		}
	}
	if err := os.RemoveAll(filepath.Join(w.JobsDir, id)); err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	return w.manage(ctx, func(bus Bus) error {
		_ = bus.Call(ctx, sdDest, sdPath, sdManager+".ResetFailedUnit", nil, UnitBase(id)+".service")
		return bus.Call(ctx, sdDest, sdPath, sdManager+".Reload", nil)
	})
}

// Run implements Writer: the job's service is started now (it does not
// wait for the run to end).
func (w *UserWriter) Run(ctx context.Context, id string) error {
	return w.manage(ctx, func(bus Bus) error {
		var job dbus.ObjectPath
		return bus.Call(ctx, sdDest, sdPath, sdManager+".StartUnit", []any{&job}, UnitBase(id)+".service", "replace")
	})
}

// SetPaused implements Writer.
func (w *UserWriter) SetPaused(ctx context.Context, id string, paused bool) error {
	return w.manage(ctx, func(bus Bus) error {
		if paused {
			return stopTimer(ctx, bus, id)
		}
		return startTimer(ctx, bus, id)
	})
}

// Script implements Writer.
func (w *UserWriter) Script(_ context.Context, id string) (string, error) {
	if !ValidID(id) {
		return "", errors.New("bad job id")
	}
	b, err := os.ReadFile(w.ScriptPath(id)) // #nosec G304 G703 -- id: 8 hex digits (ValidID above)
	if err != nil {
		return "", fmt.Errorf("scheduler: %w", err)
	}
	return string(b), nil
}

func (w *UserWriter) manage(ctx context.Context, fn func(Bus) error) error {
	bus, err := w.Bus(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: your systemd manager: %w", err)
	}
	defer func() { _ = bus.Close() }()
	return fn(bus)
}

// writeAtomic writes a file through a temporary file and a rename.
func writeAtomic(path, data string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hearth-*")
	if err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("scheduler: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("scheduler: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	return nil
}
