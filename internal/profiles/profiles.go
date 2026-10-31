// Package profiles keeps named sets of units, containers, Compose projects
// and apps to start or stop together (SPEC 18, ROADMAP 4.6), by hand or
// while a game runs (SECURITY S15a).
//
// Every step is an action of the registry, so it is validated and audited
// like the same click elsewhere, and what a step would need confirmed
// (a critical unit) is refused. Activation remembers what it really
// changed, and deactivation undoes only that.
package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Kinds of item; they are also the action kinds, but for apps.
const (
	KindUnit      = "unit"
	KindContainer = "container"
	KindProject   = "project"
	KindApp       = "app"
)

// What to do to an item.
const (
	DoStart = "start"
	DoStop  = "stop"
)

// Who activated a profile.
const (
	ByUser = "user"
	ByGame = "game"
)

// Reasons an item is blocked; the UI translates them.
const (
	ReasonSystem = "system" // it would need a typed confirmation
	ReasonError  = "error"
)

// Limits of a profile.
const (
	maxItems = 64
	maxName  = 40
	// stepTimeout bounds one step; a system unit may wait for a password.
	stepTimeout = 2 * time.Minute
	// gameGone is how many polls without a game end it: launchers restart
	// their helpers between a game's stages.
	gameGone = 2
)

// Item is one thing a profile acts on.
type Item struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Do     string `json:"do"`
}

// Store is what profiles keep (store.DB in production).
type Store interface {
	Profiles(ctx context.Context) ([]store.Profile, error)
	Profile(ctx context.Context, id int64) (store.Profile, error)
	AddProfile(ctx context.Context, p store.Profile) (int64, error)
	UpdateProfile(ctx context.Context, p store.Profile) error
	DeleteProfile(ctx context.Context, id int64) error
	SetProfileActive(ctx context.Context, id int64, at time.Time, by, undo string) error
	ClearProfileActive(ctx context.Context, id int64) error
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Actions finds registered actions (actions.Registry in production).
type Actions interface {
	Lookup(key string) (actions.Action, bool)
}

// Config configures a Manager.
type Config struct {
	Store   Store
	Actions Actions
	// Active reports whether an item is running now.
	Active func(ctx context.Context, it Item) (bool, error)
	// Playing lists the games running now.
	Playing func() ([]string, error)
	// Notify tells the user what was done without being asked.
	Notify func(ctx context.Context, summary, body string)
	Log    *slog.Logger
	Clock  func() time.Time
}

// Manager applies and restores profiles.
type Manager struct {
	cfg Config
	mu  sync.Mutex // one activation at a time

	playing bool // only Run
	missed  int
}

// New returns a Manager.
func New(cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Notify == nil {
		cfg.Notify = func(context.Context, string, string) {}
	}
	return &Manager{cfg: cfg}
}

// actionKey is the action that does it.
func actionKey(it Item) string {
	if it.Kind == KindApp {
		return "app/launch"
	}
	return it.Kind + "/" + it.Do
}

// inverse undoes it; apps are never closed (that could lose work).
func inverse(it Item) (Item, bool) {
	switch {
	case it.Kind == KindApp:
		return Item{}, false
	case it.Do == DoStart:
		it.Do = DoStop
	default:
		it.Do = DoStart
	}
	return it, true
}

// unattended reports whether it can run with nobody there: a system unit
// or a container may open a password dialog (SECURITY S15a).
func unattended(it Item) bool {
	return it.Kind == KindApp || (it.Kind == KindUnit && strings.HasPrefix(it.Target, "user/"))
}

// validate checks a profile before it is stored.
func (m *Manager) validate(name string, items []Item, auto bool) error {
	if name == "" || utf8.RuneCountInString(name) > maxName || !utf8.ValidString(name) {
		return errors.New("a name is 1 to 40 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("a name is one line")
		}
	}
	if len(items) == 0 || len(items) > maxItems {
		return errors.New("a profile has 1 to 64 items")
	}
	seen := map[Item]bool{}
	for _, it := range items {
		switch it.Kind {
		case KindUnit, KindContainer, KindProject:
			if it.Do != DoStart && it.Do != DoStop {
				return errors.New("an item either starts or stops")
			}
		case KindApp:
			if it.Do != DoStart {
				return fmt.Errorf("%s: apps are not closed by a profile: that could lose unsaved work", it.Target)
			}
		default:
			return errors.New("an item is a unit, container, project or app")
		}
		key := Item{Kind: it.Kind, Target: it.Target}
		if seen[key] {
			return fmt.Errorf("%s is listed twice", it.Target)
		}
		seen[key] = true
		a, ok := m.cfg.Actions.Lookup(actionKey(it))
		if !ok {
			return fmt.Errorf("%s: not available", it.Kind)
		}
		r := actions.Request{Target: it.Target, Params: map[string]string{}}
		if err := a.Validate(r); err != nil {
			return fmt.Errorf("%s: %w", it.Target, err)
		}
		if auto && !unattended(it) {
			return fmt.Errorf("%s: it may ask for a password, so it cannot run while a game starts", it.Target)
		}
		if it.Kind == KindUnit { // planning a unit touches nothing
			p, err := a.Plan(context.Background(), r)
			if err != nil {
				return fmt.Errorf("%s: %w", it.Target, err)
			}
			if p.Risk == actions.Destructive || len(p.Blocked) > 0 {
				return fmt.Errorf("%s: a profile cannot stop this unit", it.Target)
			}
		}
	}
	return nil
}

// ValidateImported checks a profile from a file as if it had been saved, with
// no game trigger: importing never sets one.
func (m *Manager) ValidateImported(name string, items []Item) error {
	return m.validate(name, items, false)
}

// step is one planned action.
type step struct {
	item Item
	a    actions.Action
	r    actions.Request
	plan actions.Plan
}

// prepared is what activation or deactivation will do.
type prepared struct {
	id    int64
	name  string
	steps []step
}

func toItem(it Item) actions.Item {
	return actions.Item{ID: it.Kind + ":" + it.Target, Name: it.Target, Detail: it.Do}
}

// prepare plans items in order, leaving out what is already as wanted.
func (m *Manager) prepare(ctx context.Context, items []Item, auto bool) ([]step, []actions.Blocked) {
	var steps []step
	blocked := []actions.Blocked{}
	block := func(it Item, reason string) {
		blocked = append(blocked, actions.Blocked{Item: toItem(it), Reason: reason})
	}
	for _, it := range items {
		a, ok := m.cfg.Actions.Lookup(actionKey(it))
		if !ok || (auto && !unattended(it)) {
			block(it, ReasonSystem)
			continue
		}
		r := actions.Request{Target: it.Target, Params: map[string]string{}}
		if err := a.Validate(r); err != nil {
			block(it, ReasonError)
			continue
		}
		plan, err := a.Plan(ctx, r)
		switch {
		case err != nil:
			block(it, ReasonError)
			continue
		case len(plan.Blocked) > 0:
			blocked = append(blocked, plan.Blocked...)
			continue
		case plan.Risk == actions.Destructive:
			block(it, ReasonSystem)
			continue
		}
		running, err := m.cfg.Active(ctx, it)
		if err != nil {
			block(it, ReasonError)
			continue
		}
		if running == (it.Do == DoStart) {
			continue
		}
		steps = append(steps, step{item: it, a: a, r: r, plan: plan})
	}
	return steps, blocked
}

// execute runs steps, each audited. done lists what happened, left what
// failed. A client going away does not stop it half way.
func (m *Manager) execute(ctx context.Context, profile string, steps []step) (res actions.Result, done, left []Item, err error) {
	res.Failed = []actions.Blocked{}
	ctx = context.WithoutCancel(ctx)
	for _, s := range steps {
		sctx, cancel := context.WithTimeout(ctx, stepTimeout)
		r, runErr := s.a.Run(sctx, s.r, s.plan)
		cancel()
		outcome := "ok"
		switch {
		case runErr != nil:
			outcome = "error: " + runErr.Error()
			res.Failed = append(res.Failed, actions.Blocked{Item: toItem(s.item), Reason: ReasonError})
		case len(r.Failed) > 0:
			outcome = "failed: " + r.Failed[0].Reason
			res.Failed = append(res.Failed, r.Failed...)
		case r.Done == 0:
			outcome = "nothing done"
		}
		if runErr == nil && len(r.Failed) == 0 {
			res.Done++
			done = append(done, s.item)
		} else {
			left = append(left, s.item)
		}
		if aerr := m.cfg.Store.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
			Action: s.a.Kind + "." + s.a.Name, Target: s.r.Target, Params: map[string]string{"profile": profile}, Result: outcome}); aerr != nil {
			err = aerr // the step ran; finish so that it can be restored
		}
	}
	return res, done, left, err
}

func encodeItems(items []Item) (string, error) {
	if items == nil {
		items = []Item{}
	}
	b, err := json.Marshal(items)
	return string(b), err
}

func decodeItems(s string) ([]Item, error) {
	items := []Item{}
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return nil, fmt.Errorf("profiles: %w", err)
	}
	return items, nil
}

// planActivate prepares a profile: stops first, to free what starts need.
func (m *Manager) planActivate(ctx context.Context, p store.Profile, auto bool) (prepared, []actions.Blocked, error) {
	items, err := decodeItems(p.Items)
	if err != nil {
		return prepared{}, nil, err
	}
	var stops, starts []Item
	for _, it := range items {
		if it.Do == DoStop {
			stops = append(stops, it)
		} else {
			starts = append(starts, it)
		}
	}
	steps, blocked := m.prepare(ctx, append(stops, starts...), auto)
	return prepared{id: p.ID, name: p.Name, steps: steps}, blocked, nil
}

// applyActivate runs a plan and records what will restore it.
func (m *Manager) applyActivate(ctx context.Context, pr prepared, by string) (actions.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, err := m.cfg.Store.Profile(ctx, pr.id); err != nil {
		return actions.Result{}, err
	} else if !p.ActivatedAt.IsZero() {
		return actions.Result{}, actions.Errorf(http.StatusConflict, "this profile is already active")
	}
	res, done, left, err := m.execute(ctx, pr.name, pr.steps)
	if len(done) == 0 && len(left) > 0 {
		return res, err // nothing changed: not active
	}
	undo := []Item{}
	for i := len(done) - 1; i >= 0; i-- {
		if u, ok := inverse(done[i]); ok {
			undo = append(undo, u)
		}
	}
	text, jerr := encodeItems(undo)
	if jerr != nil {
		return res, jerr
	}
	if serr := m.cfg.Store.SetProfileActive(context.WithoutCancel(ctx), pr.id, m.cfg.Clock(), by, text); serr != nil {
		return res, serr
	}
	return res, err
}

// planDeactivate prepares restoring p; blocked items are given up.
func (m *Manager) planDeactivate(ctx context.Context, p store.Profile) (prepared, []actions.Blocked, error) {
	undo, err := decodeItems(p.Undo)
	if err != nil {
		return prepared{}, nil, err
	}
	steps, blocked := m.prepare(ctx, undo, false)
	return prepared{id: p.ID, name: p.Name, steps: steps}, blocked, nil
}

// applyDeactivate restores. What failed stays to be tried again by hand.
func (m *Manager) applyDeactivate(ctx context.Context, pr prepared) (actions.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res, _, left, err := m.execute(ctx, pr.name, pr.steps)
	ctx = context.WithoutCancel(ctx)
	if len(left) == 0 {
		if serr := m.cfg.Store.ClearProfileActive(ctx, pr.id); serr != nil {
			return res, serr
		}
		return res, err
	}
	text, jerr := encodeItems(left)
	if jerr != nil {
		return res, jerr
	}
	if serr := m.cfg.Store.SetProfileActive(ctx, pr.id, m.cfg.Clock(), ByUser, text); serr != nil {
		return res, serr
	}
	return res, err
}

// Register adds profile/activate and profile/deactivate. Activating asks
// for confirmation when it stops anything: the preview is the list of what
// will really change now.
func (m *Manager) Register(reg *actions.Registry) {
	validate := func(r actions.Request) error {
		if id, err := strconv.ParseInt(r.Target, 10, 64); err != nil || id <= 0 || len(r.Params) != 0 {
			return errors.New("target must be a profile ID and params empty")
		}
		return nil
	}
	load := func(ctx context.Context, target string) (store.Profile, error) {
		id, _ := strconv.ParseInt(target, 10, 64)
		p, err := m.cfg.Store.Profile(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return p, actions.Errorf(http.StatusNotFound, "no such profile")
		}
		return p, err
	}
	toPlan := func(pr prepared, blocked []actions.Blocked) actions.Plan {
		plan := actions.Plan{Summary: pr.name, Items: []actions.Item{}, Blocked: blocked, State: pr}
		for _, s := range pr.steps {
			plan.Items = append(plan.Items, toItem(s.item))
		}
		return plan
	}
	reg.Register(actions.Action{Kind: "profile", Name: "activate", Risk: actions.Reversible, Validate: validate,
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			p, err := load(ctx, r.Target)
			if err != nil {
				return actions.Plan{}, err
			}
			if !p.ActivatedAt.IsZero() {
				return actions.Plan{}, actions.Errorf(http.StatusConflict, "this profile is already active")
			}
			pr, blocked, err := m.planActivate(ctx, p, false)
			if err != nil {
				return actions.Plan{}, err
			}
			plan := toPlan(pr, blocked)
			for _, s := range pr.steps {
				if s.item.Do == DoStop {
					plan.Risk = actions.Destructive
				}
			}
			return plan, nil
		},
		Run: func(ctx context.Context, _ actions.Request, plan actions.Plan) (actions.Result, error) {
			pr, ok := plan.State.(prepared)
			if !ok {
				return actions.Result{}, errors.New("profiles: no plan")
			}
			return m.applyActivate(ctx, pr, ByUser)
		}})
	reg.Register(actions.Action{Kind: "profile", Name: "deactivate", Risk: actions.Reversible, Validate: validate,
		Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			p, err := load(ctx, r.Target)
			if err != nil {
				return actions.Plan{}, err
			}
			if p.ActivatedAt.IsZero() {
				return actions.Plan{}, actions.Errorf(http.StatusConflict, "this profile is not active")
			}
			pr, blocked, err := m.planDeactivate(ctx, p)
			if err != nil {
				return actions.Plan{}, err
			}
			return toPlan(pr, blocked), nil
		},
		Run: func(ctx context.Context, _ actions.Request, plan actions.Plan) (actions.Result, error) {
			pr, ok := plan.State.(prepared)
			if !ok {
				return actions.Result{}, errors.New("profiles: no plan")
			}
			return m.applyDeactivate(ctx, pr)
		}})
}

// Run follows games until ctx ends: profiles set to do so are activated
// when a game starts, and restored when none has run for gameGone polls.
// This also restores what a game left behind if hearthd was not running
// when it ended.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.poll(ctx)
		}
	}
}

func (m *Manager) poll(ctx context.Context) {
	list, err := m.cfg.Store.Profiles(ctx)
	if err != nil {
		m.cfg.Log.Warn("profiles: cannot read", "err", err)
		return
	}
	var auto, byGame []store.Profile
	for _, p := range list {
		if p.AutoGame && p.ActivatedAt.IsZero() {
			auto = append(auto, p)
		}
		if p.ActivatedBy == ByGame {
			byGame = append(byGame, p)
		}
	}
	if len(auto) == 0 && len(byGame) == 0 {
		m.playing, m.missed = false, 0
		return
	}
	games, err := m.cfg.Playing()
	if err != nil {
		m.cfg.Log.Warn("profiles: cannot look for games", "err", err)
		return
	}
	if len(games) > 0 {
		m.missed = 0
		if !m.playing { // once per game: the user may deactivate during it
			m.playing = true
			for _, p := range auto {
				m.autoActivate(ctx, p)
			}
		}
		return
	}
	m.missed++
	if m.missed < gameGone {
		return
	}
	m.playing = false
	for _, p := range byGame {
		m.autoDeactivate(ctx, p)
	}
}

func (m *Manager) autoActivate(ctx context.Context, p store.Profile) {
	pr, blocked, err := m.planActivate(ctx, p, true)
	if err == nil {
		var res actions.Result
		res, err = m.applyActivate(ctx, pr, ByGame)
		if err == nil {
			m.cfg.Notify(ctx, p.Name+" is on, a game started", fmt.Sprintf("%d changed, %d failed, %d skipped.", res.Done, len(res.Failed), len(blocked)))
		}
	}
	m.autoAudit(ctx, p, "profile.activate", err)
}

func (m *Manager) autoDeactivate(ctx context.Context, p store.Profile) {
	pr, _, err := m.planDeactivate(ctx, p)
	if err == nil {
		var res actions.Result
		res, err = m.applyDeactivate(ctx, pr)
		switch {
		case err != nil:
		case len(res.Failed) > 0:
			m.cfg.Notify(ctx, p.Name+" could not restore everything", "Open Profiles in Hearth to try again.")
		default:
			m.cfg.Notify(ctx, p.Name+" is off, the game ended", fmt.Sprintf("%d restored.", res.Done))
		}
	}
	m.autoAudit(ctx, p, "profile.deactivate", err)
}

func (m *Manager) autoAudit(ctx context.Context, p store.Profile, action string, err error) {
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
		m.cfg.Log.Warn("profiles: automatic change", "profile", p.Name, "err", err)
	}
	if aerr := m.cfg.Store.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(), Action: action,
		Target: strconv.FormatInt(p.ID, 10), Params: map[string]string{"by": ByGame, "profile": p.Name}, Result: result}); aerr != nil {
		m.cfg.Log.Warn("profiles: audit", "err", aerr)
	}
}
