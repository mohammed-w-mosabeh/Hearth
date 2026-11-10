package automations

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// The triggers of ROADMAP 12.4 that are not alert metrics or mode
// sources: idle, resume, apps and games, files, SSH failures, security
// and image updates.

// Watch are their sources; each may be nil (its trigger then never fires).
type Watch struct {
	// IdleSeconds is how long the session has been idle.
	IdleSeconds func(ctx context.Context) (float64, error)
	// Resumes delivers a value each time the machine wakes from sleep.
	Resumes func(ctx context.Context) (<-chan struct{}, error)
	// RunningApps are the desktop app IDs running now (the user's own
	// processes, by their groups); Games the games running (command lines
	// of the user's own processes only, S15a).
	RunningApps func() map[string]bool
	Games       func() ([]string, error)
	// SSHFailures counts the failed SSH logins since a time, with their
	// sources (S16j).
	SSHFailures func(ctx context.Context, since time.Time) (int, []string, error)
	// SecurityUpdates counts the security updates PackageKit lists.
	SecurityUpdates func(ctx context.Context) (int, error)
	// ImageUpdates are the managed apps with a newer image found by the
	// user's own update previews (S21a).
	ImageUpdates func(ctx context.Context) (map[string]bool, error)
	// Home is the user's home, for files triggers.
	Home string
}

// SSHEvery is how often SSH failures are counted, only while an
// automation watches them.
const SSHEvery = 5 * time.Minute

// sourceState is what a 12.4 trigger saw last.
type sourceState struct {
	seen    bool
	idle    bool
	running bool
	count   int
	at      time.Time
	stacks  map[string]bool
}

// tickSources looks at the 12.4 triggers once.
func (e *Engine) tickSources(ctx context.Context, autos []loaded, now time.Time) {
	w := e.cfg.Watch
	var apps map[string]bool
	var games []string
	appsRead := false
	for _, a := range autos {
		t := a.spec.Trigger
		e.mu.Lock()
		if e.src == nil {
			e.src = map[int64]*sourceState{}
		}
		st := e.src[a.ID]
		if st == nil {
			st = &sourceState{}
			e.src[a.ID] = st
		}
		e.mu.Unlock()
		var ev *Event
		switch t.Kind {
		case "idle":
			if w.IdleSeconds == nil {
				continue
			}
			secs, err := w.IdleSeconds(ctx)
			if err != nil {
				continue
			}
			idle := secs >= float64(t.Minutes*60)
			if st.seen && idle && !st.idle {
				ev = &Event{Kind: "idle", Value: secs, Time: now}
			}
			st.idle, st.seen = idle, true
		case "app":
			if !appsRead {
				if w.RunningApps != nil {
					apps = w.RunningApps()
				}
				if w.Games != nil {
					games, _ = w.Games()
				}
				appsRead = true
			}
			on := false
			if t.Game {
				on = len(games) > 0
			} else if apps != nil {
				on = apps[t.App]
			}
			if st.seen && on != st.running && on == (t.State == "started") {
				inst := t.App
				if t.Game && len(games) > 0 {
					inst = games[0]
				}
				ev = &Event{Kind: "app", Instance: inst, Time: now}
			}
			st.running, st.seen = on, true
		case "ssh_failed":
			if w.SSHFailures == nil || (st.seen && now.Sub(st.at) < SSHEvery) {
				continue
			}
			n, sources, err := w.SSHFailures(ctx, now.Add(-time.Hour))
			if err != nil {
				continue
			}
			over := n >= t.Count
			if st.seen && over && st.count < t.Count {
				ev = &Event{Kind: "ssh_failed", Value: float64(n), Detail: strings.Join(sources, ", "), Time: now}
			}
			st.count, st.at, st.seen = n, now, true
		case "security_updates":
			if w.SecurityUpdates == nil {
				continue
			}
			n, err := w.SecurityUpdates(ctx)
			if err != nil {
				continue
			}
			if st.seen && n > st.count {
				ev = &Event{Kind: "security_updates", Value: float64(n), Time: now}
			}
			st.count, st.seen = n, true
		case "image_update":
			if w.ImageUpdates == nil {
				continue
			}
			found, err := w.ImageUpdates(ctx)
			if err != nil {
				continue
			}
			for name, has := range found {
				if st.seen && has && !st.stacks[name] && (t.Target == "" || t.Target == name) {
					ev = &Event{Kind: "image_update", Instance: name, Time: now}
					break
				}
			}
			st.stacks, st.seen = found, true
		case "files":
			if t.Event != "older_than" || (st.seen && now.Sub(st.at) < 24*time.Hour) {
				continue
			}
			dir, ok := e.folder(t.Folder)
			if !ok {
				continue
			}
			n := countOld(dir, t.Depth, t.Pattern, now.Add(-time.Duration(t.Days)*24*time.Hour))
			if n > 0 {
				ev = &Event{Kind: "files", Instance: t.Folder, Value: float64(n), Time: now}
			}
			st.at, st.seen = now, true
		}
		if ev != nil {
			e.Fire(ctx, a, *ev, "trigger")
		}
	}
}

// folder resolves "~/..." to a folder that is still inside the home after
// its links are resolved.
func (e *Engine) folder(rel string) (string, bool) {
	home := e.cfg.Watch.Home
	if home == "" {
		return "", false
	}
	full := filepath.Join(home, strings.TrimPrefix(rel, "~"))
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", false
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil || (real != realHome && !strings.HasPrefix(real, realHome+"/")) {
		return "", false
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", false
	}
	return real, true
}

// maxScan bounds a folder scan.
const maxScan = 20000

// countOld counts the regular files under dir, depth levels down at
// most, matching pattern and last changed before cut; links are never
// followed.
func countOld(dir string, depth int, pattern string, cut time.Time) int {
	n, seen := 0, 0
	base := strings.Count(dir, "/")
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		seen++
		if err != nil || seen > maxScan {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if strings.Count(p, "/")-base > depth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if ok, _ := filepath.Match(pattern, d.Name()); !ok {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().Before(cut) {
			n++
		}
		return nil
	})
	return n
}

// fileWatch holds the fsnotify watches of the files/new triggers.
type fileWatch struct {
	w       *fsnotify.Watcher
	dirs    map[string][]int64 // watched folder -> automations
	cancel  context.CancelFunc
	pattern map[int64]string
}

// watchFiles sets the fsnotify watches up again for the enabled files/new
// triggers: the folder and its subfolders down to the depth, as they are
// now (no recursion beyond it).
func (e *Engine) watchFiles(ctx context.Context, autos []loaded) {
	e.mu.Lock()
	old := e.files
	e.files = nil
	e.mu.Unlock()
	if old != nil {
		old.cancel()
		_ = old.w.Close()
	}
	var want []loaded
	for _, a := range autos {
		if a.spec.Trigger.Kind == "files" && a.spec.Trigger.Event == "new" {
			want = append(want, a)
		}
	}
	if len(want) == 0 {
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		e.cfg.Log.Warn("automations: cannot watch files", "err", err)
		return
	}
	fw := &fileWatch{w: w, dirs: map[string][]int64{}, pattern: map[int64]string{}}
	for _, a := range want {
		t := a.spec.Trigger
		dir, ok := e.folder(t.Folder)
		if !ok {
			continue
		}
		fw.pattern[a.ID] = t.Pattern
		base := strings.Count(dir, "/")
		n := 0
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			n++
			if err != nil || n > maxScan || !d.IsDir() {
				return nil
			}
			if strings.Count(p, "/")-base > t.Depth {
				return filepath.SkipDir
			}
			if len(fw.dirs) < 256 && w.Add(p) == nil {
				fw.dirs[p] = append(fw.dirs[p], a.ID)
			}
			return nil
		})
	}
	wctx, cancel := context.WithCancel(ctx)
	fw.cancel = cancel
	e.mu.Lock()
	e.files = fw
	e.mu.Unlock()
	go e.fileEvents(wctx, fw, want)
}

func (e *Engine) fileEvents(ctx context.Context, fw *fileWatch, autos []loaded) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fw.w.Events:
			if !ok {
				return
			}
			if !ev.Has(fsnotify.Create) {
				continue
			}
			fi, err := os.Lstat(ev.Name)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			ids := fw.dirs[filepath.Dir(ev.Name)]
			for _, a := range autos {
				if !slices.Contains(ids, a.ID) {
					continue
				}
				if ok, _ := filepath.Match(fw.pattern[a.ID], filepath.Base(ev.Name)); !ok {
					continue
				}
				rel := "~" + strings.TrimPrefix(ev.Name, e.homeReal())
				e.Fire(ctx, a, Event{Kind: "files", Instance: rel, Time: e.cfg.Clock()}, "trigger")
			}
		case err, ok := <-fw.w.Errors:
			if !ok {
				return
			}
			e.cfg.Log.Debug("automations: file watch", "err", err)
		}
	}
}

func (e *Engine) homeReal() string {
	r, err := filepath.EvalSymlinks(e.cfg.Watch.Home)
	if err != nil {
		return e.cfg.Watch.Home
	}
	return r
}

// watchResume fires the resume triggers each time the machine wakes.
func (e *Engine) watchResume(ctx context.Context) {
	if e.cfg.Watch.Resumes == nil {
		return
	}
	ch, err := e.cfg.Watch.Resumes(ctx)
	if err != nil {
		e.cfg.Log.Warn("automations: cannot watch for resume", "err", err)
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				e.mu.Lock()
				autos := slices.Clone(e.autos)
				e.mu.Unlock()
				for _, a := range autos {
					if a.spec.Trigger.Kind == "resume" {
						e.Fire(ctx, a, Event{Kind: "resume", Time: e.cfg.Clock()}, "trigger")
					}
				}
			}
		}
	}()
}
