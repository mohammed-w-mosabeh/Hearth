package modes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// ByUser is who activated a mode by hand, as the profile actions say it.
const ByUser = "user"

// ProfileDB is what ProfileStore keeps in the database (store.DB).
type ProfileDB interface {
	ModeNumbers(ctx context.Context, ids []string) (map[string]int64, error)
	ModeArmings(ctx context.Context) ([]store.ModeArming, error)
	ArmModeTrigger(ctx context.Context, a store.ModeArming) error
	ActiveModes(ctx context.Context) ([]store.ActiveMode, error)
	SetActiveMode(ctx context.Context, m store.ActiveMode) error
	ClearActiveMode(ctx context.Context, modeID string) error
	Audit(ctx context.Context, e store.AuditEntry) error
}

// ProfileStore serves the profile engine (profiles.Store) from mode files,
// so profiles keep working as modes (ROADMAP 10.2): a profile is a mode's
// name, items and apps, its number is the mode's, and its game trigger is on
// when the mode has one armed for the current file. What activation changed
// is the active mode's snapshot.
type ProfileStore struct {
	Set   *Set
	DB    ProfileDB
	Clock func() time.Time

	mu sync.Mutex // one write at a time
}

// snapshot is what mode_active.snapshot holds.
type snapshot struct {
	// Undo is what the mode's profile items changed, as profile items, in
	// the order that restores them.
	Undo json.RawMessage `json:"undo"`
}

func (ps *ProfileStore) now() time.Time {
	if ps.Clock != nil {
		return ps.Clock()
	}
	return time.Now()
}

// entry is a mode as a profile, with what it came from.
type entry struct {
	p    store.Profile
	mode *Mode // nil for an active mode whose file is gone or invalid
}

func (ps *ProfileStore) entries(ctx context.Context) ([]entry, error) {
	modes := ps.Set.Modes()
	active, err := ps.DB.ActiveModes(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(modes)+len(active))
	for _, m := range modes {
		ids = append(ids, m.ID)
	}
	for _, a := range active {
		ids = append(ids, a.ModeID)
	}
	nums, err := ps.DB.ModeNumbers(ctx, ids)
	if err != nil {
		return nil, err
	}
	armings, err := ps.DB.ModeArmings(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]store.ActiveMode{}
	for _, a := range active {
		byID[a.ModeID] = a
	}
	out := make([]entry, 0, len(ids))
	for i := range modes {
		m := &modes[i]
		items, err := json.Marshal(m.ActionItems())
		if err != nil {
			return nil, err
		}
		e := entry{mode: m, p: store.Profile{ID: nums[m.ID], Name: m.Name, Items: string(items), AutoGame: gameArmed(*m, armings)}}
		if a, ok := byID[m.ID]; ok {
			if err := setActive(&e.p, a); err != nil {
				return nil, err
			}
			delete(byID, m.ID)
		}
		out = append(out, e)
	}
	// A mode still active after its file went away or broke stays listed,
	// so what it changed can be restored.
	for _, a := range active {
		if _, ok := byID[a.ModeID]; !ok {
			continue
		}
		e := entry{p: store.Profile{ID: nums[a.ModeID], Name: a.ModeID, Items: "[]"}}
		if err := setActive(&e.p, a); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].p.Name), strings.ToLower(out[j].p.Name)
		if a != b {
			return a < b
		}
		return out[i].p.ID < out[j].p.ID
	})
	return out, nil
}

func setActive(p *store.Profile, a store.ActiveMode) error {
	var s snapshot
	if err := json.Unmarshal([]byte(a.Snapshot), &s); err != nil {
		return fmt.Errorf("modes: snapshot of %s: %w", a.ModeID, err)
	}
	p.ActivatedAt, p.ActivatedBy, p.Undo = a.Since, a.By, "[]"
	if len(s.Undo) > 0 {
		p.Undo = string(s.Undo)
	}
	return nil
}

// gameArmed reports whether m's game trigger is armed for this file.
func gameArmed(m Mode, armings []store.ModeArming) bool {
	for i, t := range m.Triggers {
		if t.Kind != TriggerGame {
			continue
		}
		for _, a := range armings {
			if a.ModeID == m.ID && a.Trigger == i && a.FileHash == m.Hash {
				return true
			}
		}
	}
	return false
}

func (ps *ProfileStore) find(ctx context.Context, id int64) (entry, error) {
	list, err := ps.entries(ctx)
	if err != nil {
		return entry{}, err
	}
	for _, e := range list {
		if e.p.ID == id {
			return e, nil
		}
	}
	return entry{}, store.ErrNotFound
}

// modeID is the mode an entry stands for.
func (e entry) modeID() string {
	if e.mode != nil {
		return e.mode.ID
	}
	return e.p.Name
}

// Profiles lists the modes as profiles, by name.
func (ps *ProfileStore) Profiles(ctx context.Context) ([]store.Profile, error) {
	list, err := ps.entries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.Profile, 0, len(list))
	for _, e := range list {
		out = append(out, e.p)
	}
	return out, nil
}

// Profile returns the mode numbered id as a profile, or store.ErrNotFound.
func (ps *ProfileStore) Profile(ctx context.Context, id int64) (store.Profile, error) {
	e, err := ps.find(ctx, id)
	return e.p, err
}

// apply sets a profile's name, items, stacks, apps and game trigger on m,
// keeping the rest of the mode.
func apply(m Mode, p store.Profile) (Mode, error) {
	var items []Item
	if err := json.Unmarshal([]byte(p.Items), &items); err != nil {
		return m, fmt.Errorf("modes: items: %w", err)
	}
	m.Name, m.Items, m.Stacks, m.Session.Open = p.Name, []Item{}, []StackAction{}, nil
	for _, it := range items {
		switch it.Kind {
		case KindApp:
			m.Session.Open = append(m.Session.Open, it.Target)
		case KindStack:
			m.Stacks = append(m.Stacks, StackAction{Name: it.Target, Do: it.Do})
		default:
			m.Items = append(m.Items, it)
		}
	}
	has := m.HasGame()
	switch {
	case p.AutoGame && !has:
		m.Triggers = append(slices.Clone(m.Triggers), Trigger{Kind: TriggerGame})
	case !p.AutoGame && has:
		m.Triggers = slices.DeleteFunc(slices.Clone(m.Triggers), func(t Trigger) bool { return t.Kind == TriggerGame })
	}
	return m, nil
}

// save writes m over the file that had hash (none for a new file), reads
// the folder again, and arms the game trigger when on is true: the user
// turned it on by saving in Hearth (SECURITY S15b).
func (ps *ProfileStore) save(ctx context.Context, m Mode, hash string, on bool) error {
	raw, err := Encode(m)
	if err != nil {
		return err
	}
	if _, err := Parse(m.ID, raw); err != nil {
		return fmt.Errorf("modes: %s: %w", m.ID, err)
	}
	root, err := openDir(ps.Set.cfg.Dir)
	if err != nil {
		return err
	}
	err = writeFile(root, m.ID, raw, hash)
	_ = root.Close()
	if err != nil {
		return err
	}
	ps.Set.Load(ctx)
	saved, ok := ps.Set.Mode(m.ID)
	if !ok {
		reason := "it is not valid"
		for _, fe := range ps.Set.Errors() {
			if fe.File == m.ID+".toml" {
				reason = fe.Error
			}
		}
		return fmt.Errorf("modes: %s.toml was written, but %s", m.ID, reason)
	}
	if !on {
		return nil
	}
	for i, t := range saved.Triggers {
		if t.Kind == TriggerGame {
			if err := ps.DB.ArmModeTrigger(ctx, store.ModeArming{ModeID: saved.ID, Trigger: i, FileHash: saved.Hash, ArmedAt: ps.now()}); err != nil {
				return err
			}
		}
	}
	return nil
}

// AddProfile writes a new mode file for p and returns its number.
func (ps *ProfileStore) AddProfile(ctx context.Context, p store.Profile) (int64, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	nums, err := ps.DB.ModeNumbers(ctx, nil)
	if err != nil {
		return 0, err
	}
	root, err := openDir(ps.Set.cfg.Dir)
	if err != nil {
		return 0, err
	}
	id, err := freeID(Slug(p.Name), func(id string) bool { _, used := nums[id]; return used || exists(root, id) })
	_ = root.Close()
	if err != nil {
		return 0, err
	}
	m, err := apply(Mode{ID: id, Stacks: []StackAction{}, Triggers: []Trigger{}}, p)
	if err != nil {
		return 0, err
	}
	if err := ps.save(ctx, m, "", p.AutoGame); err != nil {
		return 0, err
	}
	nums, err = ps.DB.ModeNumbers(ctx, []string{id})
	if err != nil {
		return 0, err
	}
	return nums[id], nil
}

// UpdateProfile rewrites the mode numbered p.ID with p's name, items, apps
// and game trigger; the rest of the mode and whether it is active stay.
func (ps *ProfileStore) UpdateProfile(ctx context.Context, p store.Profile) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	e, err := ps.find(ctx, p.ID)
	if err != nil {
		return err
	}
	if e.mode == nil {
		return store.ErrNotFound // its file is gone or broken: nothing to edit
	}
	m, err := apply(*e.mode, p)
	if err != nil {
		return err
	}
	return ps.save(ctx, m, e.mode.Hash, p.AutoGame)
}

// DeleteProfile moves the mode file numbered id to the deleted folder.
func (ps *ProfileStore) DeleteProfile(ctx context.Context, id int64) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	e, err := ps.find(ctx, id)
	if err != nil {
		return err
	}
	if e.mode == nil {
		return store.ErrNotFound
	}
	root, err := openDir(ps.Set.cfg.Dir)
	if err != nil {
		return err
	}
	err = trashFile(root, e.mode.ID, e.mode.Hash, ps.now())
	_ = root.Close()
	if err != nil {
		return err
	}
	ps.Set.Load(ctx)
	return nil
}

// SetProfileActive records the mode numbered id as active, with undo (JSON
// profile items) as what restores it.
func (ps *ProfileStore) SetProfileActive(ctx context.Context, id int64, at time.Time, by, undo string) error {
	e, err := ps.find(ctx, id)
	if err != nil {
		return err
	}
	if !json.Valid([]byte(undo)) {
		return errors.New("modes: undo is not JSON")
	}
	snap, err := json.Marshal(snapshot{Undo: json.RawMessage(undo)})
	if err != nil {
		return err
	}
	hash := ""
	if e.mode != nil {
		hash = e.mode.Hash
	} else if list, err := ps.DB.ActiveModes(ctx); err == nil {
		for _, a := range list {
			if a.ModeID == e.modeID() {
				hash = a.FileHash
			}
		}
	}
	return ps.DB.SetActiveMode(ctx, store.ActiveMode{ModeID: e.modeID(), FileHash: hash, Since: at, By: by, Snapshot: string(snap)})
}

// ClearProfileActive records that the mode numbered id is not active.
func (ps *ProfileStore) ClearProfileActive(ctx context.Context, id int64) error {
	e, err := ps.find(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return ps.DB.ClearActiveMode(ctx, e.modeID())
}

// Audit records an entry in the audit log.
func (ps *ProfileStore) Audit(ctx context.Context, e store.AuditEntry) error {
	return ps.DB.Audit(ctx, e)
}
