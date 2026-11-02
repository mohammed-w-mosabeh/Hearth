// Package integrity is the Integrity watch on the Security page (ROADMAP 7.3):
// a baseline of sensitive files the user owns and of the names in root's
// schedule and sudo folders, an alert when one changes, a diff for text files
// and "accept as new baseline". It runs entirely as the user and only reads
// (SECURITY S16i): nothing here reaches the broker.
package integrity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Metric is the alert catalog's id.
const Metric = "integrity.changed"

// The kinds of entry.
const (
	KindFile       = "file"
	KindLarge      = "large" // over maxHash: size and times only
	KindLink       = "link"  // the link's target, never followed
	KindOther      = "other" // a socket, a fifo, a device
	KindUnreadable = "unreadable"
	KindName       = "name" // a name in a system folder: never opened
)

const (
	settingSince = "integrity.since"
	scanEvery    = 15 * time.Minute
	firstScan    = time.Minute
	racyWindow   = 2 * time.Second
	maxEntries   = 2000
	maxDepth     = 4
	maxHash      = 64 << 20 // a bigger file is compared by size and times
	maxText      = 64 << 10
	maxChanges   = 500
	maxNames     = 200
	homePrefix   = "~/"
)

// HomeFiles are watched where they are; a missing one is a file that may
// appear. HomeDirs are walked, without following links.
var (
	HomeFiles = []string{
		".ssh/authorized_keys", ".ssh/config",
		".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile",
		".zshrc", ".zshenv", ".zprofile", ".zlogin", ".zlogout",
		".config/fish/config.fish",
	}
	HomeDirs = []string{".config/autostart", ".config/systemd/user", ".local/bin"}
	// SystemDirs are listed by name only.
	SystemDirs = []string{
		"/etc/sudoers.d", "/etc/cron.d", "/etc/cron.hourly", "/etc/cron.daily",
		"/etc/cron.weekly", "/etc/cron.monthly", "/etc/cron.yearly",
	}
)

// Store keeps the baseline (store.DB in production).
type Store interface {
	IntegrityBaseline(ctx context.Context) (map[string]store.IntegrityEntry, error)
	IntegrityText(ctx context.Context, path string) (*string, error)
	PutIntegrity(ctx context.Context, e store.IntegrityEntry) error
	DeleteIntegrity(ctx context.Context, path string) error
	Setting(ctx context.Context, key string) (json.RawMessage, bool, error)
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
}

// Auditor appends to the audit log (store.DB in production).
type Auditor interface {
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Mux is the subset of api.Mux the routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// Config configures a Manager.
type Config struct {
	Store      Store
	Audit      Auditor
	Observe    func(ctx context.Context, now time.Time, v map[string]float64)
	Home       string
	SystemDirs []string // default SystemDirs
	Now        func() time.Time
	Log        *slog.Logger
}

// Manager scans and serves the routes.
type Manager struct {
	cfg Config
	mu  sync.Mutex // one scan or accept at a time
}

// New returns a Manager.
func New(cfg Config) *Manager {
	if cfg.SystemDirs == nil {
		cfg.SystemDirs = SystemDirs
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Manager{cfg: cfg}
}

// entry is one item found by a scan. real is its path on disk, which may not
// be valid UTF-8 and so may differ from the shown path. The zero text means
// the entry was not read for its text.
type entry struct {
	store.IntegrityEntry
	real string
}

// found is what a scan saw.
type found struct {
	items     map[string]entry
	blind     map[string]bool // roots that could not be read: nothing there counts as removed
	system    []SystemDir
	truncated bool
}

// SystemDir is one system folder's names.
type SystemDir struct {
	Dir       string   `json:"dir"`
	Readable  bool     `json:"readable"`
	Names     []string `json:"names"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Change is one difference from the baseline.
type Change struct {
	Path string `json:"path"`
	Root string `json:"root"`
	Kind string `json:"kind"` // added, changed or removed
	Hash string `json:"hash"` // of what is there now; accepting names it
}

// View is GET /api/v1/security/integrity's answer.
type View struct {
	Since     int64       `json:"since"`   // unix ms the baseline began; 0 before the first scan
	Scanned   int64       `json:"scanned"` // unix ms of this scan
	Watched   int         `json:"watched"`
	Truncated bool        `json:"truncated,omitempty"`
	Changes   []Change    `json:"changes"`
	More      int         `json:"more,omitempty"` // changes past the list's bound
	System    []SystemDir `json:"system"`
	Error     string      `json:"error,omitempty"`
}

func shown(name string) string { return strings.ToValidUTF8(name, "�") }

func hashHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func times(fi fs.FileInfo) (mtime, ctime int64) {
	mtime = fi.ModTime().UnixNano()
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ctime = st.Ctim.Nano()
	}
	return mtime, ctime
}

// sameStat tells a scan it may reuse the hash. ctime cannot be set from user
// space, so a file rewritten with its old mtime is still read. A file touched
// within racyWindow of being accepted is always read: the clock's tick is
// coarse, so a rewrite of the same size just after could carry the same times.
func sameStat(a store.IntegrityEntry, size, mtime, ctime int64) bool {
	settled := ctime/int64(time.Millisecond)+racyWindow.Milliseconds() < a.Accepted
	return settled && a.Size == size && a.MTime == mtime && a.CTime == ctime && (a.Kind == KindFile || a.Kind == KindLarge)
}

// read opens a regular file through root, so no link on the way can lead out
// of the home, and hashes it. With wantText it also returns masked text, for
// a small text file holding no private key.
func read(root *os.Root, name string, fi fs.FileInfo, wantText bool) (kind, hash string, text *string) {
	size := fi.Size()
	mtime, ctime := times(fi)
	if size > maxHash {
		return KindLarge, hashHex([]byte(strconv.FormatInt(size, 10) + ":" + strconv.FormatInt(mtime, 10) + ":" + strconv.FormatInt(ctime, 10))), nil
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return KindUnreadable, "", nil
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	var head strings.Builder
	var w io.Writer = h
	if wantText {
		w = io.MultiWriter(h, &limited{b: &head, left: maxText + 1})
	}
	if _, err := io.Copy(w, io.LimitReader(f, maxHash)); err != nil {
		return KindUnreadable, "", nil
	}
	hash = hex.EncodeToString(h.Sum(nil))
	if s := head.String(); wantText && len(s) <= maxText && utf8.ValidString(s) && !strings.ContainsRune(s, 0) && !strings.Contains(s, "PRIVATE KEY") {
		masked := maskText(s)
		text = &masked
	}
	return KindFile, hash, text
}

// limited keeps the first bytes written and quietly drops the rest.
type limited struct {
	b    *strings.Builder
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.left > 0 {
		q := p[:min(len(p), l.left)]
		l.b.Write(q)
		l.left -= len(q)
	}
	return len(p), nil
}

// inspect makes the entry for one item of the home.
func inspect(root *os.Root, name, shownPath, rootLabel string, base store.IntegrityEntry, known, wantText bool) (entry, bool) {
	e := entry{IntegrityEntry: store.IntegrityEntry{Path: shownPath, Root: rootLabel}, real: name}
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return e, false
	}
	if err != nil {
		e.Kind = KindUnreadable
		return e, true
	}
	e.Size = fi.Size()
	e.MTime, e.CTime = times(fi)
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		e.Kind = KindLink
		target, err := root.Readlink(name)
		if err != nil {
			e.Kind = KindUnreadable
			return e, true
		}
		e.Hash = hashHex([]byte(target))
		if wantText {
			t := cleanLine(target)
			e.Text = &t
		}
	case fi.Mode().IsRegular():
		if !wantText && known && sameStat(base, e.Size, e.MTime, e.CTime) {
			e.Kind, e.Hash = base.Kind, base.Hash
			return e, true
		}
		e.Kind, e.Hash, e.Text = read(root, name, fi, wantText)
	default:
		e.Kind, e.Hash = KindOther, hashHex([]byte(fi.Mode().Type().String()))
	}
	return e, true
}

// walk adds a watched folder of the home, breadth first and bounded.
func walk(ctx context.Context, root *os.Root, dir string, base map[string]store.IntegrityEntry, out *found) {
	label := homePrefix + dir
	type level struct {
		dir   string
		depth int
	}
	queue := []level{{dir, 0}}
	for len(queue) > 0 {
		if ctx.Err() != nil {
			out.blind[label] = true
			return
		}
		cur := queue[0]
		queue = queue[1:]
		d, err := root.Open(cur.dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			out.blind[label] = true
			continue
		}
		list, err := d.ReadDir(maxEntries + 1)
		_ = d.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			out.blind[label] = true
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
		for _, de := range list {
			name := path.Join(cur.dir, de.Name())
			if de.IsDir() {
				if cur.depth+1 < maxDepth {
					queue = append(queue, level{name, cur.depth + 1})
				} else {
					out.truncated = true
				}
				continue
			}
			if len(out.items) >= maxEntries {
				out.truncated = true
				return
			}
			p := homePrefix + shown(name)
			b, known := base[p]
			if e, ok := inspect(root, name, p, label, b, known, false); ok {
				out.items[p] = e
			}
		}
	}
}

// scan looks at everything watched, reading only what has been touched.
func (m *Manager) scan(ctx context.Context, base map[string]store.IntegrityEntry) found {
	out := found{items: map[string]entry{}, blind: map[string]bool{}, system: []SystemDir{}}
	root, err := os.OpenRoot(m.cfg.Home)
	if err != nil {
		for _, f := range HomeFiles {
			out.blind[homePrefix+f] = true
		}
		for _, d := range HomeDirs {
			out.blind[homePrefix+d] = true
		}
	} else {
		defer func() { _ = root.Close() }()
		for _, f := range HomeFiles {
			p := homePrefix + f
			b, known := base[p]
			if e, ok := inspect(root, f, p, p, b, known, false); ok {
				out.items[p] = e
			}
		}
		for _, d := range HomeDirs {
			walk(ctx, root, d, base, &out)
		}
	}
	for _, dir := range m.cfg.SystemDirs {
		s := SystemDir{Dir: dir, Names: []string{}}
		list, err := os.ReadDir(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			s.Readable = true
		case err != nil:
			out.blind[dir] = true
		default:
			s.Readable = true
			for i, de := range list {
				name := cleanLine(shown(de.Name()))
				if i < maxNames {
					s.Names = append(s.Names, name)
				} else {
					s.Truncated = true
				}
				if len(out.items) >= maxEntries {
					out.truncated = true
					continue
				}
				p := dir + "/" + name
				out.items[p] = entry{IntegrityEntry: store.IntegrityEntry{Path: p, Root: dir, Kind: KindName}}
			}
		}
		out.system = append(out.system, s)
	}
	return out
}

// differs is the one comparison: a touched file with the same content is no
// change.
func differs(a store.IntegrityEntry, b entry) bool { return a.Kind != b.Kind || a.Hash != b.Hash }

// compare lists what moved from the baseline. Nothing counts as removed from
// a root the scan could not read, or when the scan stopped at its bound.
func compare(base map[string]store.IntegrityEntry, f found) []Change {
	out := []Change{}
	for p, e := range f.items {
		b, known := base[p]
		switch {
		case !known:
			out = append(out, Change{Path: p, Root: e.Root, Kind: "added", Hash: e.Hash})
		case differs(b, e):
			out = append(out, Change{Path: p, Root: e.Root, Kind: "changed", Hash: e.Hash})
		}
	}
	if !f.truncated {
		for p, b := range base {
			if _, ok := f.items[p]; !ok && !f.blind[b.Root] {
				out = append(out, Change{Path: p, Root: b.Root, Kind: "removed"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (m *Manager) since(ctx context.Context) (int64, error) {
	raw, ok, err := m.cfg.Store.Setting(ctx, settingSince)
	if err != nil || !ok {
		return 0, err
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, err
	}
	return v, nil
}

// settle reads an entry's text and stores it as accepted. A file that moved
// since the scan is left for the next one.
func (m *Manager) settle(ctx context.Context, root *os.Root, e entry, now int64) (store.IntegrityEntry, bool, error) {
	if e.Kind != KindName && root != nil {
		fresh, ok := inspect(root, e.real, e.Path, e.Root, store.IntegrityEntry{}, false, true)
		if !ok || differs(store.IntegrityEntry{Kind: e.Kind, Hash: e.Hash}, fresh) {
			return store.IntegrityEntry{}, false, nil
		}
		e = fresh
	}
	e.Accepted = now
	return e.IntegrityEntry, true, m.cfg.Store.PutIntegrity(ctx, e.IntegrityEntry)
}

// establish takes the first baseline. Whatever is there now is trusted, which
// the page says; the alert starts from the next scan.
func (m *Manager) establish(ctx context.Context, f found) error {
	root, err := os.OpenRoot(m.cfg.Home)
	if err != nil {
		root = nil
	} else {
		defer func() { _ = root.Close() }()
	}
	now := m.cfg.Now().UnixMilli()
	for _, e := range f.items {
		if _, _, err := m.settle(ctx, root, e, now); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(now)
	if err != nil {
		return err
	}
	return m.cfg.Store.SetSetting(ctx, settingSince, raw)
}

// look scans, takes the baseline if there is none, and tells the alert engine
// which watched items differ: an incident per item, so a hundred new files in
// ~/.local/bin are one alert, and it ends once they are accepted. Instances
// come from the fixed lists and hold no user name.
func (m *Manager) look(ctx context.Context) (View, found, map[string]store.IntegrityEntry, error) {
	v := View{Changes: []Change{}, System: []SystemDir{}, Scanned: m.cfg.Now().UnixMilli()}
	since, err := m.since(ctx)
	if err != nil {
		return v, found{}, nil, err
	}
	base, err := m.cfg.Store.IntegrityBaseline(ctx)
	if err != nil {
		return v, found{}, nil, err
	}
	f := m.scan(ctx, base)
	if ctx.Err() != nil {
		return v, f, base, ctx.Err()
	}
	if since == 0 {
		if err := m.establish(ctx, f); err != nil {
			return v, f, base, err
		}
		if since, err = m.since(ctx); err != nil {
			return v, f, base, err
		}
		if base, err = m.cfg.Store.IntegrityBaseline(ctx); err != nil {
			return v, f, base, err
		}
	}
	changes := compare(base, f)
	news := map[string]float64{}
	for _, c := range changes {
		news[c.Root] = 1
	}
	m.cfg.Observe(ctx, m.cfg.Now(), news)
	v.Since, v.System, v.Truncated, v.Watched = since, f.system, f.truncated, len(base)
	if len(changes) > maxChanges {
		v.More = len(changes) - maxChanges
		changes = changes[:maxChanges]
	}
	v.Changes = changes
	return v, f, base, nil
}

func (m *Manager) view(ctx context.Context) View {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, _, _, err := m.look(ctx)
	if err != nil {
		m.cfg.Log.Warn("integrity: scan", "err", err)
		v.Error = "the scan failed"
	}
	return v
}

// Run scans until ctx ends: soon after start, then every 15 minutes. A scan
// mostly stats a few dozen files.
func (m *Manager) Run(ctx context.Context) {
	wait := time.NewTimer(firstScan)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wait.C:
			if v := m.view(ctx); v.Error != "" && ctx.Err() == nil {
				m.cfg.Log.Debug("integrity: scan failed")
			}
			wait.Reset(scanEvery)
		}
	}
}

// DiffView is GET /api/v1/security/integrity/diff's answer. Note says why
// there are no lines: "notext" (binary, large, unreadable or a name) or
// "hidden" (only lines that were masked changed).
type DiffView struct {
	Path      string `json:"path"`
	Lines     []Line `json:"lines"`
	Truncated bool   `json:"truncated,omitempty"`
	Note      string `json:"note,omitempty"`
}

// diff answers for a path the watch knows. The path is looked up, never
// opened as given, so this cannot read any other file.
func (m *Manager) diff(ctx context.Context, p string) (DiffView, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	base, err := m.cfg.Store.IntegrityBaseline(ctx)
	if err != nil {
		return DiffView{}, false, err
	}
	f := m.scan(ctx, base)
	e, here := f.items[p]
	b, known := base[p]
	if !here && !known {
		return DiffView{}, false, nil
	}
	out := DiffView{Path: p, Lines: []Line{}}
	var before, after *string
	empty := ""
	if known {
		if b.Kind == KindName {
			out.Note = "notext"
			return out, true, nil
		}
		if before, err = m.cfg.Store.IntegrityText(ctx, p); err != nil {
			return out, false, err
		}
	} else {
		before = &empty
	}
	if here {
		if e.Kind == KindName {
			out.Note = "notext"
			return out, true, nil
		}
		root, err := os.OpenRoot(m.cfg.Home)
		if err != nil {
			out.Note = "notext"
			return out, true, nil
		}
		defer func() { _ = root.Close() }()
		fresh, ok := inspect(root, e.real, p, e.Root, store.IntegrityEntry{}, false, true)
		if ok {
			after = fresh.Text
		}
	} else {
		after = &empty
	}
	if before == nil || after == nil {
		out.Note = "notext"
		return out, true, nil
	}
	lines, truncated, visible := Diff(*before, *after)
	if !visible {
		out.Note = "hidden"
		return out, true, nil
	}
	out.Lines, out.Truncated = lines, truncated
	return out, true, nil
}

// AcceptItem names one change as the page showed it.
type AcceptItem struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// accept makes the named changes the baseline. Each must still be what the
// page showed: a change that arrived after the page drew stays pending, so
// nothing is accepted unseen.
func (m *Manager) accept(ctx context.Context, items []AcceptItem) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	base, err := m.cfg.Store.IntegrityBaseline(ctx)
	if err != nil {
		return 0, err
	}
	f := m.scan(ctx, base)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	pending := map[string]Change{}
	for _, c := range compare(base, f) {
		pending[c.Path] = c
	}
	root, err := os.OpenRoot(m.cfg.Home)
	if err != nil {
		root = nil
	} else {
		defer func() { _ = root.Close() }()
	}
	now, done := m.cfg.Now().UnixMilli(), 0
	for _, it := range items {
		c, ok := pending[it.Path]
		if !ok || c.Hash != it.Hash {
			continue
		}
		// The audit comes first: a change the log cannot hold is not made.
		if err := m.cfg.Audit.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
			Action: "security.integrity.accept", Target: c.Path, Result: "ok: " + c.Kind}); err != nil {
			return done, err
		}
		if c.Kind == "removed" {
			if err := m.cfg.Store.DeleteIntegrity(ctx, c.Path); err != nil {
				return done, err
			}
			done++
			continue
		}
		_, stored, err := m.settle(ctx, root, f.items[c.Path], now)
		if err != nil {
			return done, err
		}
		if stored {
			done++
		}
	}
	return done, nil
}

func (m *Manager) handleDiff(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" || len(p) > 4096 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	d, ok, err := m.diff(r.Context(), p)
	if err != nil {
		m.cfg.Log.Warn("integrity: diff", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not compare the file"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not watched"})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (m *Manager) handleAccept(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Items []AcceptItem `json:"items"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || len(p.Items) == 0 || len(p.Items) > maxChanges {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if _, err := m.accept(r.Context(), p.Items); err != nil {
		m.cfg.Log.Warn("integrity: accept", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update the baseline"})
		return
	}
	writeJSON(w, http.StatusOK, m.view(r.Context()))
}

// Register adds the routes.
func (m *Manager) Register(mux Mux) {
	mux.HandleFunc("GET /api/v1/security/integrity", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.view(r.Context()))
	})
	mux.HandleFunc("GET /api/v1/security/integrity/diff", m.handleDiff)
	mux.HandleFunc("POST /api/v1/security/integrity/accept", m.handleAccept)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
