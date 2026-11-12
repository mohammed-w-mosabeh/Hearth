// Package diskusage is the folder size explorer (SPEC 9): the sizes of a
// folder's children, like du, for folders the user owns.
//
// A scan never follows symlinks and never leaves the filesystem it
// starts on (a FUSE mount such as rclone inside the home would otherwise
// be walked over the network). Sizes are allocated blocks, with hard
// links counted once.
//
// Homes hold hundreds of thousands of files (a minute or more to walk, and
// hearthd.service is limited to 10% CPU), so a scan is a background job:
// the first request starts it and every request returns its progress,
// children filling in as they finish. A job nobody asks about for a while
// is cancelled; one job runs at a time. Finished folders, and the folders
// one level below them (measured on the way), are cached, so opening a
// subfolder or going back up is instant.
package diskusage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

// Entry is one child of the scanned folder.
type Entry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`  // bytes allocated
	Files int64  `json:"files"` // files below it (1 for a file)
	Dir   bool   `json:"dir,omitempty"`
	Link  bool   `json:"link,omitempty"`     // a symlink (not followed)
	Other bool   `json:"other_fs,omitempty"` // another filesystem is mounted here (not counted)
	// Pending is set while a folder is still being measured.
	Pending bool `json:"pending,omitempty"`
	// Nav is false when the entry cannot be opened in the explorer (not a
	// directory, another filesystem, or a name that is not valid UTF-8).
	Nav bool `json:"nav"`
}

// Result is a scanned folder, finished or in progress.
type Result struct {
	Path  string  `json:"path"`
	Root  string  `json:"root"` // the allowed root it is in
	Size  int64   `json:"size"`
	Files int64   `json:"files"`
	Items []Entry `json:"items"` // largest first, at most MaxItems
	// Rest sums the children beyond MaxItems.
	Rest       *Entry `json:"rest,omitempty"`
	Unreadable int64  `json:"unreadable"` // folders that could not be read
	Running    bool   `json:"running,omitempty"`
	// Partial: the scan was stopped before the end; sizes are too small.
	Partial bool  `json:"partial,omitempty"`
	Done    int   `json:"done"`  // folders measured so far
	Total   int   `json:"total"` // folders to measure
	TookMs  int64 `json:"took_ms"`
	Cached  bool  `json:"cached,omitempty"`
	At      int64 `json:"at"` // when it was measured, Unix ms
}

var ErrOutside = errors.New("diskusage: not inside a folder you own")

// readDir is os.ReadDir; a test holds a walk open through it.
var readDir = os.ReadDir

// Config configures a Scanner.
type Config struct {
	// Roots lists the folders that may be explored (the home, user-owned
	// mounts). Paths are checked after symlinks are resolved.
	Roots func() []string
	// MaxItems bounds the children listed; default 200.
	MaxItems int
	// Workers walk subfolders in parallel; default 4.
	Workers int
	// Idle cancels a job nobody asked about for this long; default 20 s.
	Idle time.Duration
	// MaxTime bounds one job; default 15 min.
	MaxTime time.Duration
	// CacheTTL keeps results; default 10 min, at most cacheSize folders.
	CacheTTL time.Duration
}

// Scanner runs scans.
type Scanner struct {
	cfg Config
	now func() time.Time

	mu    sync.Mutex
	job   *job // the running one, if any
	cache map[string]Result
}

const cacheSize = 256

// New returns a Scanner.
func New(cfg Config) *Scanner {
	if cfg.MaxItems == 0 {
		cfg.MaxItems = 200
	}
	if cfg.Workers == 0 {
		cfg.Workers = 4
	}
	if cfg.Idle == 0 {
		cfg.Idle = 20 * time.Second
	}
	if cfg.MaxTime == 0 {
		cfg.MaxTime = 15 * time.Minute
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 10 * time.Minute
	}
	return &Scanner{cfg: cfg, now: time.Now, cache: map[string]Result{}}
}

// Resolve checks that path is an absolute folder inside an allowed root
// once symlinks are resolved, and returns the resolved path and its root.
func (s *Scanner) Resolve(path string) (string, string, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", "", ErrOutside
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", "", err
	}
	best := ""
	for _, r := range s.cfg.Roots() {
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		if (real == rr || strings.HasPrefix(real, rr+"/") || rr == "/") && len(rr) > len(best) {
			best = rr
		}
	}
	if best == "" {
		return "", "", ErrOutside
	}
	fi, err := os.Stat(real) // #nosec G703 -- resolved and checked to be inside an allowed root above
	if err != nil {
		return "", "", err
	}
	if !fi.IsDir() {
		return "", "", errors.New("diskusage: not a folder")
	}
	return real, best, nil
}

// Get returns path's sizes: from the cache, from the job measuring it,
// or from a job it starts (cancelling any other). fresh ignores the cache.
func (s *Scanner) Get(path string, fresh bool) (Result, error) {
	real, root, err := s.Resolve(path)
	if err != nil {
		return Result{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if j := s.job; j != nil && j.path == real {
		if !fresh || j.started.After(now.Add(-time.Second)) {
			return j.snapshot(now), nil
		}
	}
	if r, ok := s.cache[real]; ok && !fresh && now.Sub(time.UnixMilli(r.At)) < s.cfg.CacheTTL {
		r.Cached = true
		return r, nil
	}
	if s.job != nil {
		s.job.cancel()
	}
	j := s.start(real, root, now)
	s.job = j
	return j.snapshot(now), nil
}

// Close cancels the running job (on shutdown).
func (s *Scanner) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil {
		s.job.cancel()
	}
}

func (s *Scanner) remember(r Result) {
	// s.mu is held.
	now := s.now()
	for k, c := range s.cache {
		if now.Sub(time.UnixMilli(c.At)) >= s.cfg.CacheTTL {
			delete(s.cache, k)
		}
	}
	for len(s.cache) >= cacheSize {
		oldest := ""
		for k, c := range s.cache {
			if oldest == "" || c.At < s.cache[oldest].At {
				oldest = k
			}
		}
		delete(s.cache, oldest)
	}
	s.cache[r.Path] = r
}

// job measures one folder.
type job struct {
	s       *Scanner
	path    string
	root    string
	started time.Time
	cancel  context.CancelFunc

	mu         sync.Mutex
	items      []Entry // files and other entries are final; dirs fill in
	pending    int     // folders not measured yet
	total      int
	unreadable int64
	lastPoll   time.Time
	finished   bool
	partial    bool
	took       time.Duration
}

func (s *Scanner) start(path, root string, now time.Time) *job {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.MaxTime)
	j := &job{s: s, path: path, root: root, started: now, cancel: cancel, lastPoll: now}
	go j.run(ctx)
	return j
}

// snapshot is the job's progress as a Result; asking for it keeps the
// job alive (lastPoll).
func (j *job) snapshot(now time.Time) Result {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lastPoll = now
	r := Result{Path: j.path, Root: j.root, Items: append([]Entry{}, j.items...), Unreadable: j.unreadable,
		Running: !j.finished, Partial: j.partial, Done: j.total - j.pending, Total: j.total, At: j.started.UnixMilli()}
	if j.finished {
		r.TookMs = j.took.Milliseconds()
	} else {
		r.TookMs = now.Sub(j.started).Milliseconds()
	}
	return finish(r, j.s.cfg.MaxItems)
}

// finish totals and orders a result's items.
func finish(r Result, maxItems int) Result {
	r.Size, r.Files = 0, 0
	for _, it := range r.Items {
		r.Size += it.Size
		r.Files += it.Files
	}
	sort.SliceStable(r.Items, func(i, k int) bool {
		a, b := r.Items[i], r.Items[k]
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		return a.Name < b.Name
	})
	if len(r.Items) > maxItems {
		rest := Entry{}
		for _, it := range r.Items[maxItems:] {
			rest.Size += it.Size
			rest.Files += it.Files
		}
		r.Rest = &rest
		r.Items = r.Items[:maxItems]
	}
	return r
}

// walker is shared by a job's workers.
type walker struct {
	ctx   context.Context
	dev   uint64
	mu    sync.Mutex
	links map[[2]uint64]bool // dev, inode of files with several links
	n     atomic.Int64       // entries seen, to check ctx now and then
}

// stopped reports, every 256 entries across all workers, whether the job
// was cancelled.
func (w *walker) stopped() bool {
	return w.n.Add(1)%256 == 0 && w.ctx.Err() != nil
}

// size returns the allocated bytes, or 0 for a hard link already counted.
func (w *walker) size(st *syscall.Stat_t) int64 {
	if st.Nlink > 1 && st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		k := [2]uint64{st.Dev, st.Ino}
		w.mu.Lock()
		seen := w.links[k]
		w.links[k] = true
		w.mu.Unlock()
		if seen {
			return 0
		}
	}
	return st.Blocks * 512
}

// list reads a folder's entries, files before folders, so a file
// hard-linked again further down keeps its size where the user looks.
func list(path string) ([]os.DirEntry, error) {
	entries, err := readDir(path)
	sort.SliceStable(entries, func(i, j int) bool { return !entries[i].IsDir() && entries[j].IsDir() })
	return entries, err
}

// entry stats one child; dirs come back with Pending set and no size.
func (w *walker) entry(dir string, e os.DirEntry) (Entry, *syscall.Stat_t, bool) {
	var st syscall.Stat_t
	if err := syscall.Lstat(filepath.Join(dir, e.Name()), &st); err != nil {
		return Entry{}, nil, false // gone meanwhile
	}
	it := Entry{Name: e.Name()}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		it.Dir = true
		if st.Dev != w.dev {
			it.Other = true
		} else {
			it.Pending, it.Nav = true, utf8.ValidString(e.Name())
		}
	case syscall.S_IFLNK:
		it.Link, it.Size, it.Files = true, w.size(&st), 1
	default:
		it.Size, it.Files = w.size(&st), 1
	}
	return it, &st, true
}

// measure sums everything below path. When kids is not nil it also
// fills it with path's children, measured.
func (w *walker) measure(path string, st *syscall.Stat_t, kids *[]Entry, unreadable *int64) (size, files int64) {
	size = w.size(st)
	if kids != nil {
		// This folder's children are listed: files first, as at the top.
		entries, err := list(path)
		if err != nil {
			*unreadable++
			return size, 0
		}
		for _, e := range entries {
			if w.stopped() {
				return size, files
			}
			it, ok := w.child(path, e, unreadable)
			if ok {
				size, files = size+it.Size, files+it.Files
				*kids = append(*kids, it)
			}
		}
		return size, files
	}
	// Deeper down only totals matter: read in batches, so a folder with a
	// million entries is never held in memory at once.
	f, err := os.Open(path) // #nosec G304 -- below a folder Resolve allowed
	if err != nil {
		*unreadable++
		return size, 0
	}
	defer func() { _ = f.Close() }()
	for {
		entries, err := f.ReadDir(256)
		for _, e := range entries {
			if w.stopped() {
				return size, files
			}
			it, ok := w.child(path, e, unreadable)
			if ok {
				size, files = size+it.Size, files+it.Files
			}
		}
		if err != nil { // io.EOF at the end
			return size, files
		}
	}
}

// child stats and, for a folder, measures one entry of dir.
func (w *walker) child(dir string, e os.DirEntry, unreadable *int64) (Entry, bool) {
	it, cst, ok := w.entry(dir, e)
	if ok && it.Pending {
		it.Pending = false
		it.Size, it.Files = w.measure(filepath.Join(dir, e.Name()), cst, nil, unreadable)
	}
	return it, ok
}

func (j *job) run(ctx context.Context) {
	defer j.cancel()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go j.watchdog(ctx, stop)

	var st syscall.Stat_t
	entries, err := list(j.path)
	if err == nil {
		err = syscall.Lstat(j.path, &st)
	}
	w := &walker{ctx: ctx, dev: st.Dev, links: map[[2]uint64]bool{}}
	type work struct {
		i  int
		st *syscall.Stat_t
	}
	var todo []work
	j.mu.Lock()
	if err != nil {
		j.unreadable = 1
	}
	for _, e := range entries {
		it, cst, ok := w.entry(j.path, e)
		if !ok {
			continue
		}
		if it.Pending {
			todo = append(todo, work{len(j.items), cst})
		}
		j.items = append(j.items, it)
	}
	j.total, j.pending = len(todo), len(todo)
	j.mu.Unlock()

	queue := make(chan work)
	var wg sync.WaitGroup
	for range j.s.cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for wk := range queue {
				j.mu.Lock()
				name := j.items[wk.i].Name
				j.mu.Unlock()
				var kids []Entry
				var unreadable int64
				p := filepath.Join(j.path, name)
				size, files := w.measure(p, wk.st, &kids, &unreadable)
				if ctx.Err() != nil {
					return // a half-measured folder is not shown as done
				}
				j.mu.Lock()
				it := &j.items[wk.i]
				it.Size, it.Files, it.Pending = size, files, false
				j.pending--
				j.unreadable += unreadable
				j.mu.Unlock()
				// The subfolder's own listing, for opening it instantly.
				sub := finish(Result{Path: p, Root: j.root, Items: kids, Unreadable: unreadable, At: time.Now().UnixMilli()}, j.s.cfg.MaxItems)
				j.s.mu.Lock()
				j.s.remember(sub)
				j.s.mu.Unlock()
			}
		}()
	}
feed:
	for _, wk := range todo {
		select {
		case queue <- wk:
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()

	j.mu.Lock()
	j.finished, j.took = true, time.Since(j.started)
	j.partial = ctx.Err() != nil
	j.mu.Unlock()
	if !j.partial {
		r := j.snapshot(time.Now())
		j.s.mu.Lock()
		j.s.remember(r)
		j.s.mu.Unlock()
	}
	j.s.mu.Lock()
	if j.s.job == j {
		j.s.job = nil
	}
	j.s.mu.Unlock()
}

// watchdog stops the job once nobody has asked about it for Idle.
func (j *job) watchdog(ctx context.Context, stop context.CancelFunc) {
	tick := time.NewTicker(j.s.cfg.Idle / 4)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			j.mu.Lock()
			idle := now.Sub(j.lastPoll) > j.s.cfg.Idle
			j.mu.Unlock()
			if idle {
				stop()
				return
			}
		}
	}
}

// UserRoots returns the folders the user may explore: home, and mount
// points (other than /) whose root folder the user owns, such as a USB
// disk mounted for them.
func UserRoots(home string, mounts func() ([]string, error)) func() []string {
	return func() []string {
		roots := []string{home}
		points, err := mounts()
		if err != nil {
			return roots
		}
		uid := uint32(os.Getuid()) // #nosec G115 -- uids fit in 32 bits
		for _, p := range points {
			if p == "/" || p == home || strings.HasPrefix(home, p+"/") {
				continue
			}
			var st syscall.Stat_t
			if syscall.Lstat(p, &st) == nil && st.Uid == uid && st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
				roots = append(roots, p)
			}
		}
		return roots
	}
}

// IsNotExist reports a missing path (for the HTTP status).
func IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
