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
	// KindStack is a managed app (a mode's [[stack]], ROADMAP 10.4): its
	// steps run together in one stack.batch, with one polkit check.
	KindStack = "stack"
	// KindSetting is a session setting a mode sets (ROADMAP 10.5): Target
	// names it (Settings), Do is the value. Restoring puts back the value
	// read before, only while the setting still has the value read after.
	KindSetting = "setting"
	// KindConnection is a NetworkManager connection by UUID: start connects,
	// stop disconnects.
	KindConnection = "connection"
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
	// ReasonChanged: the setting was changed by hand after the switch, so
	// restoring leaves it as it is.
	ReasonChanged = "changed"
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
	// Expect, on a setting in a restore list, is the value the switch left;
	// a setting that no longer has it is not restored.
	Expect string `json:"expect,omitempty"`
}

// Settings reads session settings and names the action that sets one
// (modes.SessionSettings in production).
type Settings interface {
	// Current is the setting's value now, in the form Request takes.
	Current(ctx context.Context, name string) (string, error)
	// Request is the registered action and request that set name to value.
	Request(name, value string) (key string, r actions.Request, err error)
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
	// Batch, if set, runs every stack step of an activation or restore in
	// one call (stack.batch): one polkit check for all of them, which may
	// show a dialog only when interactive is true. It returns, per item, ""
	// when done or the reason it failed. Without it, stack steps run one by
	// one through their actions.
	Batch func(ctx context.Context, items []Item, interactive bool) []string
	// Settings, if set, allows setting items.
	Settings Settings
	// Defer, if set, takes the stack steps an automatic switch would make
	// (ROADMAP 10.7): they need authorization, which nobody is there to
	// give, so they wait for the user to approve them; the rest of the
	// switch runs. Without it, an automatic switch leaves stacks out and a
	// profile that has them cannot have the game trigger.
	Defer func(ctx context.Context, p store.Profile, by string, items []Item) error
	Log   *slog.Logger
	Clock func() time.Time
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

// request is the action that does it, and its request.
func (m *Manager) request(it Item) (string, actions.Request, error) {
	r := actions.Request{Target: it.Target, Params: map[string]string{}}
	switch it.Kind {
	case KindApp:
		return "app/launch", r, nil
	case KindConnection:
		if it.Do == DoStart {
			return "network/connect", r, nil
		}
		return "network/disconnect", r, nil
	case KindSetting:
		if m.cfg.Settings == nil {
			return "", r, errors.New("session settings are not available")
		}
		return m.cfg.Settings.Request(it.Target, it.Do)
	}
	return it.Kind + "/" + it.Do, r, nil
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
// or a container may open a password dialog (SECURITY S15a). Session
// settings and connections are the user's own (S15b).
func unattended(it Item) bool {
	switch it.Kind {
	case KindApp, KindSetting, KindConnection:
		return true
	}
	return it.Kind == KindUnit && strings.HasPrefix(it.Target, "user/")
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
		case KindUnit, KindContainer, KindProject, KindStack:
			if it.Do != DoStart && it.Do != DoStop {
				return errors.New("an item either starts or stops")
			}
		case KindApp:
			if it.Do != DoStart {
				return fmt.Errorf("%s: apps are not closed by a profile: that could lose unsaved work", it.Target)
			}
		case KindConnection:
			if it.Do != DoStart && it.Do != DoStop {
				return errors.New("a connection either connects or disconnects")
			}
		case KindSetting:
			if m.cfg.Settings == nil {
				return fmt.Errorf("%s: session settings are not available", it.Target)
			}
		default:
			return errors.New("an item is a unit, container, project, stack, setting, connection or app")
		}
		if it.Expect != "" {
			return errors.New("expect is only for restoring")
		}
		once := Item{Kind: it.Kind, Target: it.Target}
		if seen[once] {
			return fmt.Errorf("%s is listed twice", it.Target)
		}
		seen[once] = true
		key, r, err := m.request(it)
		if err != nil {
			return fmt.Errorf("%s: %w", it.Target, err)
		}
		a, ok := m.cfg.Actions.Lookup(key)
		if !ok {
			return fmt.Errorf("%s: not available", it.Kind)
		}
		if err := a.Validate(r); err != nil {
			return fmt.Errorf("%s: %w", it.Target, err)
		}
		if auto && !unattended(it) && (it.Kind != KindStack || m.cfg.Defer == nil) {
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

// ValidateItems checks a mode's items and apps as a saved profile's, with
// the game trigger's limits when game is true (ROADMAP 10.2).
func (m *Manager) ValidateItems(items []Item, game bool) error {
	return m.validate("mode", items, game)
}

// step is one planned action.
type step struct {
	item Item
	a    actions.Action
	r    actions.Request
	plan actions.Plan
	prev string // a setting's value before the step
}

// prepared is what activation or deactivation will do.
type prepared struct {
	id    int64
	name  string
	steps []step
	// deferred are stack items an automatic switch leaves for approval.
	deferred []Item
}

func toItem(it Item) actions.Item {
	return actions.Item{ID: it.Kind + ":" + it.Target, Name: it.Target, Detail: it.Do}
}

// prepare plans items in order, leaving out what is already as wanted.
func (m *Manager) prepare(ctx context.Context, items []Item, auto bool) ([]step, []actions.Blocked, []Item) {
	var steps []step
	var deferred []Item
	blocked := []actions.Blocked{}
	block := func(it Item, reason string) {
		blocked = append(blocked, actions.Blocked{Item: toItem(it), Reason: reason})
	}
	for _, it := range items {
		key, r, rerr := m.request(it)
		a, ok := m.cfg.Actions.Lookup(key)
		wait := auto && it.Kind == KindStack && m.cfg.Defer != nil
		if rerr != nil || !ok || (auto && !unattended(it) && !wait) {
			block(it, ReasonSystem)
			continue
		}
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
		if it.Kind == KindSetting {
			// What it is now is what restoring puts back; a setting
			// changed by hand since the switch is left alone.
			cur, err := m.cfg.Settings.Current(ctx, it.Target)
			switch {
			case err != nil:
				block(it, ReasonError)
			case it.Expect != "" && cur != it.Expect:
				block(it, ReasonChanged)
			case cur == it.Do:
			default:
				steps = append(steps, step{item: it, a: a, r: r, plan: plan, prev: cur})
			}
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
		if wait {
			deferred = append(deferred, it)
			continue
		}
		steps = append(steps, step{item: it, a: a, r: r, plan: plan})
	}
	return steps, blocked, deferred
}

// execute runs steps, each audited. done lists what happened, left what
// failed. A client going away does not stop it half way. Stack steps run
// together through Batch when the first of them is reached; interactive
// says whether its polkit check may show a dialog.
//
// undo lists, in the order they ran, what restores each step that was done:
// the inverse of a start or stop, or a setting's earlier value with the
// value it has now as Expect. Apps have none.
func (m *Manager) execute(ctx context.Context, profile string, steps []step, interactive bool) (res actions.Result, done, left, undo []Item, err error) {
	res.Failed = []actions.Blocked{}
	ctx = context.WithoutCancel(ctx)
	var batched map[int]string // step index to its batch reason
	for i, s := range steps {
		var r actions.Result
		var runErr error
		if s.item.Kind == KindStack && m.cfg.Batch != nil {
			if batched == nil {
				batched = m.runBatch(ctx, steps, interactive)
			}
			r = actions.Result{Done: 1, Failed: []actions.Blocked{}}
			if reason := batched[i]; reason != "" {
				r = actions.Result{Failed: []actions.Blocked{{Item: toItem(s.item), Reason: reason}}}
			}
		} else {
			sctx, cancel := context.WithTimeout(ctx, stepTimeout)
			r, runErr = s.a.Run(sctx, s.r, s.plan)
			cancel()
		}
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
			if s.item.Kind == KindSetting {
				now, cerr := m.cfg.Settings.Current(ctx, s.item.Target)
				if cerr != nil {
					now = s.item.Do
				}
				undo = append(undo, Item{Kind: KindSetting, Target: s.item.Target, Do: s.prev, Expect: now})
			} else if u, ok := inverse(s.item); ok {
				undo = append(undo, u)
			}
		} else {
			left = append(left, s.item)
		}
		if aerr := m.cfg.Store.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
			Action: s.a.Kind + "." + s.a.Name, Target: s.r.Target, Params: map[string]string{"profile": profile}, Result: outcome}); aerr != nil {
			err = aerr // the step ran; finish so that it can be restored
		}
	}
	return res, done, left, undo, err
}

// runBatch runs every stack step in one Batch call, in order, and maps each
// step's index to "" (done) or the reason it failed.
func (m *Manager) runBatch(ctx context.Context, steps []step, interactive bool) map[int]string {
	var idx []int
	var items []Item
	for i, s := range steps {
		if s.item.Kind == KindStack {
			idx, items = append(idx, i), append(items, s.item)
		}
	}
	bctx, cancel := context.WithTimeout(ctx, time.Duration(len(items))*stepTimeout)
	reasons := m.cfg.Batch(bctx, items, interactive)
	cancel()
	out := make(map[int]string, len(idx))
	for j, i := range idx {
		out[i] = ReasonError
		if j < len(reasons) {
			out[i] = reasons[j]
		}
	}
	return out
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
	steps, blocked, deferred := m.prepare(ctx, append(stops, starts...), auto)
	return prepared{id: p.ID, name: p.Name, steps: steps, deferred: deferred}, blocked, nil
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
	res, done, left, undone, err := m.execute(ctx, pr.name, pr.steps, by == ByUser)
	if len(done) == 0 && len(left) > 0 {
		return res, err // nothing changed: not active
	}
	undo := []Item{}
	for i := len(undone) - 1; i >= 0; i-- {
		undo = append(undo, undone[i])
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
	steps, blocked, _ := m.prepare(ctx, undo, false)
	return prepared{id: p.ID, name: p.Name, steps: steps}, blocked, nil
}

// applyDeactivate restores. What failed stays to be tried again by hand.
// interactive is false when nobody asked for it (the game ended).
func (m *Manager) applyDeactivate(ctx context.Context, pr prepared, interactive bool) (actions.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res, _, left, _, err := m.execute(ctx, pr.name, pr.steps, interactive)
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
			return m.applyDeactivate(ctx, pr, true)
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
				m.autoActivate(ctx, p, ByGame, "a game started")
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

// autoActivate switches p on with nobody there: only what can run
// unattended, never a dialog. by is who switched it (a trigger's kind) and
// why says so in the notification.
func (m *Manager) autoActivate(ctx context.Context, p store.Profile, by, why string) {
	pr, blocked, err := m.planActivate(ctx, p, true)
	if err == nil && len(pr.steps) == 0 && len(pr.deferred) > 0 {
		// Only stacks would change: the mode is on, and they wait.
		err = m.cfg.Store.SetProfileActive(context.WithoutCancel(ctx), pr.id, m.cfg.Clock(), by, "[]")
		if err == nil {
			m.cfg.Notify(ctx, p.Name+" is on, "+why, "Its apps wait for your approval in Hearth.")
		}
	} else if err == nil {
		var res actions.Result
		res, err = m.applyActivate(ctx, pr, by)
		if err == nil {
			body := fmt.Sprintf("%d changed, %d failed, %d skipped.", res.Done, len(res.Failed), len(blocked))
			if len(pr.deferred) > 0 {
				body += fmt.Sprintf(" %d apps wait for your approval in Hearth.", len(pr.deferred))
			}
			m.cfg.Notify(ctx, p.Name+" is on, "+why, body)
		}
	}
	if err == nil && len(pr.deferred) > 0 {
		if derr := m.cfg.Defer(ctx, p, by, pr.deferred); derr != nil {
			m.cfg.Log.Warn("profiles: cannot ask for approval", "profile", p.Name, "err", derr)
		}
	}
	m.autoAuditBy(ctx, p, "profile.activate", by, err)
}

// ApplyDeferred runs items an automatic switch of profile id left for
// approval, now that the user approved them (ROADMAP 10.7): the stack steps
// in one batch that may prompt once, then their inverses join the front of
// the restore list, so revert undoes them first. The profile must still be
// active. Items no longer needed are left out.
func (m *Manager) ApplyDeferred(ctx context.Context, id int64, items []Item) (actions.Result, error) {
	for _, it := range items {
		if it.Kind != KindStack || (it.Do != DoStart && it.Do != DoStop) || it.Expect != "" {
			return actions.Result{}, errors.New("profiles: only stack steps wait for approval")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.cfg.Store.Profile(ctx, id)
	if err != nil {
		return actions.Result{}, err
	}
	if p.ActivatedAt.IsZero() {
		return actions.Result{}, actions.Errorf(http.StatusConflict, "this mode is no longer on")
	}
	undo, err := decodeItems(p.Undo)
	if err != nil {
		return actions.Result{}, err
	}
	steps, blocked, _ := m.prepare(ctx, items, false)
	res, _, _, undone, err := m.execute(ctx, p.Name, steps, true)
	res.Blocked = append(res.Blocked, blocked...)
	front := []Item{}
	for i := len(undone) - 1; i >= 0; i-- {
		front = append(front, undone[i])
	}
	text, jerr := encodeItems(append(front, undo...))
	if jerr != nil {
		return res, jerr
	}
	if serr := m.cfg.Store.SetProfileActive(context.WithoutCancel(ctx), p.ID, p.ActivatedAt, p.ActivatedBy, text); serr != nil {
		return res, serr
	}
	return res, err
}

// AutoActivate switches profile id on for a trigger (ROADMAP 10.6): as
// the game trigger does, unattended items only and no dialog. by names the
// trigger; a profile already active is left as it is. by must not be
// ByUser, which would allow a dialog.
func (m *Manager) AutoActivate(ctx context.Context, id int64, by, why string) error {
	if by == ByUser || by == "" {
		return errors.New("profiles: an automatic switch names its trigger")
	}
	p, err := m.cfg.Store.Profile(ctx, id)
	if err != nil {
		return err
	}
	if !p.ActivatedAt.IsZero() {
		return nil
	}
	m.autoActivate(ctx, p, by, why)
	return nil
}

func (m *Manager) autoDeactivate(ctx context.Context, p store.Profile) {
	pr, _, err := m.planDeactivate(ctx, p)
	if err == nil {
		var res actions.Result
		res, err = m.applyDeactivate(ctx, pr, false)
		switch {
		case err != nil:
		case len(res.Failed) > 0:
			m.cfg.Notify(ctx, p.Name+" could not restore everything", "Open Profiles in Hearth to try again.")
		default:
			m.cfg.Notify(ctx, p.Name+" is off, the game ended", fmt.Sprintf("%d restored.", res.Done))
		}
	}
	m.autoAuditBy(ctx, p, "profile.deactivate", ByGame, err)
}

func (m *Manager) autoAuditBy(ctx context.Context, p store.Profile, action, by string, err error) {
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
		m.cfg.Log.Warn("profiles: automatic change", "profile", p.Name, "err", err)
	}
	if aerr := m.cfg.Store.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(), Action: action,
		Target: strconv.FormatInt(p.ID, 10), Params: map[string]string{"by": by, "profile": p.Name}, Result: result}); aerr != nil {
		m.cfg.Log.Warn("profiles: audit", "err", aerr)
	}
}
