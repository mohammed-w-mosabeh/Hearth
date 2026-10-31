// Package backup keeps copies of Hearth's own data: VACUUM INTO copies of
// the database, on demand and weekly, and one JSON file of settings that can
// be exported and imported (SECURITY S21f).
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/profiles"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const (
	keep      = 4 // database copies kept
	every     = 7 * 24 * time.Hour
	nameStamp = "20060102-150405"
)

var nameRe = regexp.MustCompile(`^hearth-[0-9]{8}-[0-9]{6}\.db$`)

// Weekly is the weekly report's switch (reports.Weekly in production).
type Weekly interface {
	Enabled(ctx context.Context) (bool, error)
	SetEnabled(ctx context.Context, on bool) error
}

// Config configures a Manager.
type Config struct {
	DB  *store.DB
	Dir string // of the database copies
	// ValidateProfile checks a profile as if the user had saved it.
	ValidateProfile func(name string, items []profiles.Item) error
	Weekly          Weekly
	// RulesChanged and TilesChanged run after an import.
	RulesChanged func(ctx context.Context)
	TilesChanged func()
	Log          *slog.Logger
	Clock        func() time.Time
}

// Manager makes and lists copies, and exports and imports settings.
type Manager struct {
	cfg Config
	mu  sync.Mutex // one copy at a time
}

// New returns a Manager.
func New(cfg Config) *Manager {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Manager{cfg: cfg}
}

// Copy is one database copy.
type Copy struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time int64  `json:"time"` // unix ms
}

// List returns the copies, newest first.
func (m *Manager) List() ([]Copy, error) {
	entries, err := os.ReadDir(m.cfg.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Copy{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: list: %w", err)
	}
	out := []Copy{}
	for _, e := range entries {
		if !nameRe.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, Copy{Name: e.Name(), Size: fi.Size(), Time: fi.ModTime().UnixMilli()})
	}
	// The name holds the time, so it sorts by age.
	slices.SortFunc(out, func(a, b Copy) int {
		switch {
		case a.Name > b.Name:
			return -1
		case a.Name < b.Name:
			return 1
		}
		return 0
	})
	return out, nil
}

// Create writes a new copy and removes all but the newest few. The file
// appears under its final name only when complete.
func (m *Manager) Create(ctx context.Context) (Copy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.cfg.Dir, 0o700); err != nil {
		return Copy{}, fmt.Errorf("backup: %w", err)
	}
	if err := os.Chmod(m.cfg.Dir, 0o700); err != nil { // #nosec G302 -- a directory
		return Copy{}, fmt.Errorf("backup: %w", err)
	}
	name := "hearth-" + m.cfg.Clock().Format(nameStamp) + ".db"
	final := filepath.Join(m.cfg.Dir, name)
	tmp := final + ".tmp"
	_ = os.Remove(tmp) // VACUUM INTO refuses an existing file
	defer func() { _ = os.Remove(tmp) }()
	if err := m.cfg.DB.VacuumInto(ctx, tmp); err != nil {
		return Copy{}, err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return Copy{}, fmt.Errorf("backup: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return Copy{}, fmt.Errorf("backup: %w", err)
	}
	fi, err := os.Stat(final)
	if err != nil {
		return Copy{}, fmt.Errorf("backup: %w", err)
	}
	list, err := m.List()
	if err != nil {
		return Copy{}, err
	}
	for i := keep; i < len(list); i++ {
		if err := os.Remove(filepath.Join(m.cfg.Dir, list[i].Name)); err != nil {
			m.cfg.Log.Warn("backup: an old copy stays", "name", list[i].Name, "err", err)
		}
	}
	return Copy{Name: name, Size: fi.Size(), Time: fi.ModTime().UnixMilli()}, nil
}

// due reports whether the newest copy is a week old, or there is none.
func (m *Manager) due() (bool, error) {
	list, err := m.List()
	if err != nil {
		return false, err
	}
	return len(list) == 0 || m.cfg.Clock().Sub(time.UnixMilli(list[0].Time)) >= every, nil
}

// Run makes the weekly copy, checking every interval until ctx ends. An
// hourly check survives a machine that is off at any fixed moment.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if due, err := m.due(); err != nil {
			m.cfg.Log.Warn("backup: weekly copy", "err", err)
		} else if due {
			if _, err := m.Create(ctx); err != nil && ctx.Err() == nil {
				m.cfg.Log.Warn("backup: weekly copy", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// HandleList serves GET /api/v1/backup.
func (m *Manager) HandleList(w http.ResponseWriter, _ *http.Request) {
	list, err := m.List()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the backups could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": m.cfg.Dir, "copies": list})
}
