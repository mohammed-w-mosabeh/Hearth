package modes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/mohammed-w-mosabeh/hearth/internal/profiles"
)

// maxFiles is how many mode files are read; the rest are reported.
const maxFiles = 64

// FileError is a mode file that was ignored, and why.
type FileError struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// Store is what the set keeps in the database (store.DB in production).
type Store interface {
	DisarmModeStale(ctx context.Context, modeID, hash string) (int64, error)
}

// Config configures a Set.
type Config struct {
	// Dir is the modes directory, created 0700 when missing.
	Dir string
	// Check validates what a mode asks of the profile actions, as a saved
	// profile is checked (profiles.Manager.ValidateImported in production).
	Check func(items []profiles.Item) error
	// Store disarms the triggers of a file that changed or went away.
	Store Store
	// Changed is called after a reload that changed something.
	Changed func()
	Log     *slog.Logger
}

// Set is the mode files as last read.
type Set struct {
	cfg Config

	mu     sync.RWMutex
	modes  []Mode
	errs   []FileError
	hashes map[string]string // file ID to hash, valid or not
}

// NewSet returns a Set; call Load or Run to read the files.
func NewSet(cfg Config) *Set {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Changed == nil {
		cfg.Changed = func() {}
	}
	return &Set{cfg: cfg, modes: []Mode{}, errs: []FileError{}, hashes: map[string]string{}}
}

// Modes lists the valid modes by name.
func (s *Set) Modes() []Mode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.modes)
}

// Mode returns the valid mode id.
func (s *Set) Mode(id string) (Mode, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.modes {
		if m.ID == id {
			return m, true
		}
	}
	return Mode{}, false
}

// Errors lists the files that were ignored, by file name.
func (s *Set) Errors() []FileError {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.errs)
}

// Load reads every mode file again. It reports whether anything changed.
// A file that changed, became invalid or went away has its triggers
// disarmed (SECURITY S15b).
func (s *Set) Load(ctx context.Context) bool {
	modes, errs, hashes := s.read()
	s.mu.Lock()
	old := s.hashes
	changed := !equalHashes(old, hashes) || !slices.Equal(s.errs, errs)
	s.modes, s.errs, s.hashes = modes, errs, hashes
	s.mu.Unlock()
	if s.cfg.Store != nil {
		valid := map[string]string{}
		for _, m := range modes {
			valid[m.ID] = m.Hash
		}
		for id := range unionKeys(old, valid) {
			n, err := s.cfg.Store.DisarmModeStale(ctx, id, valid[id])
			if err != nil {
				s.cfg.Log.Warn("modes: cannot disarm", "mode", id, "err", err)
			} else if n > 0 {
				s.cfg.Log.Info("modes: file changed, triggers disarmed", "mode", id, "count", n)
			}
		}
	}
	return changed
}

func equalHashes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func unionKeys(a, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// read reads the directory. hashes holds every file read, valid or not.
func (s *Set) read() (modes []Mode, errs []FileError, hashes map[string]string) {
	modes, errs, hashes = []Mode{}, []FileError{}, map[string]string{}
	root, err := os.OpenRoot(s.cfg.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		errs = append(errs, FileError{File: "", Error: "the modes folder cannot be read"})
		s.cfg.Log.Warn("modes: cannot open folder", "err", err)
		return
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		errs = append(errs, FileError{File: "", Error: "the modes folder cannot be read"})
		s.cfg.Log.Warn("modes: cannot list folder", "err", err)
		return
	}
	n := 0
	names := map[string]string{} // folded mode name to the file holding it
	for _, e := range entries {  // sorted by file name
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".toml") {
			continue // editors' swap and backup files
		}
		shown := Clean(name)
		if n == maxFiles {
			errs = append(errs, FileError{File: shown, Error: fmt.Sprintf("only %d mode files are read", maxFiles)})
			continue
		}
		n++
		id := strings.TrimSuffix(name, ".toml")
		raw, err := readFile(root, name)
		if err != nil {
			errs = append(errs, FileError{File: shown, Error: err.Error()})
			continue
		}
		if ValidID(id) {
			hashes[id] = Hash(raw)
		}
		m, err := Parse(id, raw)
		if err == nil && s.cfg.Check != nil {
			if items := m.ProfileItems(); len(items) > 0 {
				if cerr := s.cfg.Check(items); cerr != nil {
					err = errors.New(Clean(cerr.Error()))
				}
			}
		}
		if err == nil {
			key := strings.ToLower(m.Name)
			if other, ok := names[key]; ok {
				err = fmt.Errorf("%s already has the name %q", Clean(other), m.Name)
			}
		}
		if err != nil {
			errs = append(errs, FileError{File: shown, Error: err.Error()})
			continue
		}
		names[strings.ToLower(m.Name)] = name
		modes = append(modes, m)
	}
	sort.SliceStable(modes, func(i, j int) bool { return strings.ToLower(modes[i].Name) < strings.ToLower(modes[j].Name) })
	return
}

// readFile reads a regular file of root, never through a link, bounded.
func readFile(root *os.Root, name string) ([]byte, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, errors.New("the file cannot be read")
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file: links and folders are ignored")
	}
	if fi.Size() > MaxFileSize {
		return nil, fmt.Errorf("the file is larger than %d KiB", MaxFileSize>>10)
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("the file cannot be read")
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || !os.SameFile(fi, st) {
		return nil, errors.New("the file changed while it was read")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, errors.New("the file cannot be read")
	}
	if len(raw) > MaxFileSize {
		return nil, fmt.Errorf("the file is larger than %d KiB", MaxFileSize>>10)
	}
	return raw, nil
}

// Debounce and recheck intervals of Run.
var (
	debounceDelay = 300 * time.Millisecond
	recheckEvery  = time.Minute
)

// Run creates the folder if needed, reads it, then reads it again after each
// change (debounced) until ctx ends. If the folder is replaced or the watch
// fails, it is watched again on the next recheck.
func (s *Set) Run(ctx context.Context) {
	if err := os.MkdirAll(s.cfg.Dir, 0o700); err != nil {
		s.cfg.Log.Warn("modes: cannot create folder", "err", err)
	}
	if s.Load(ctx) {
		s.cfg.Changed()
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		s.cfg.Log.Warn("modes: cannot watch for changes", "err", err)
		return
	}
	defer func() { _ = w.Close() }()
	watching := w.Add(s.cfg.Dir) == nil
	tick := time.NewTicker(recheckEvery)
	defer tick.Stop()
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Name == s.cfg.Dir && ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				watching = false
			}
			debounce = time.After(debounceDelay)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			s.cfg.Log.Warn("modes: watch error", "err", err)
			debounce = time.After(debounceDelay) // events may have been lost
		case <-tick.C:
			if !watching || len(w.WatchList()) == 0 {
				_ = w.Remove(s.cfg.Dir)
				watching = w.Add(s.cfg.Dir) == nil
				debounce = time.After(0)
			}
		case <-debounce:
			debounce = nil
			if s.Load(ctx) {
				s.cfg.Changed()
			}
		}
	}
}
