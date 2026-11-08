// Package modes reads mode definitions (SPEC 23, ROADMAP 10.1): TOML files
// in ~/.config/hearth/modes/, one mode per file, extending profiles
// (SECURITY S15a, S15b).
//
// A mode file is untrusted. It holds names, never commands or specs: every
// key comes from a closed list, every value is checked for its shape, and a
// stack is reached only by its name. What a file must not be able to fake
// (which triggers are armed, the active mode, snapshots, approvals) is kept
// in the database, never in the file.
package modes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/session"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Limits of a mode file.
const (
	MaxFileSize = 64 << 10
	maxName     = 40
	maxOpen     = 16
	maxConnect  = 8
	maxStacks   = 32 // one stack.batch call
	maxItems    = 64
	maxTriggers = 8
	maxTarget   = 255
)

// Kinds of action item, as the profile actions name them.
const (
	KindUnit      = "unit"
	KindContainer = "container"
	KindProject   = "project"
	KindApp       = "app"
	KindStack     = "stack" // a [[stack]], run by stack.batch
)

// What an item or a stack action does.
const (
	DoStart = "start"
	DoStop  = "stop"
)

// Stack verbs a mode may ask for.
const (
	StackStart = "start"
	StackStop  = "stop"
)

// Trigger kinds.
const (
	TriggerSchedule = "schedule"
	TriggerWiFi     = "wifi"
	TriggerBattery  = "battery"
	TriggerDisplay  = "display"
	TriggerGame     = "game"
)

// Power profiles of power-profiles-daemon.
var powerProfiles = map[string]bool{"power-saver": true, "balanced": true, "performance": true}

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Session holds the session settings a mode sets. A nil or empty field is
// left alone.
type Session struct {
	Wallpaper    string   `toml:"wallpaper,omitempty" json:"wallpaper,omitempty"`
	DoNotDisturb *bool    `toml:"do_not_disturb,omitempty" json:"do_not_disturb,omitempty"`
	Brightness   *int     `toml:"brightness,omitempty" json:"brightness,omitempty"` // percent, 1–100
	PowerProfile string   `toml:"power_profile,omitempty" json:"power_profile,omitempty"`
	Open         []string `toml:"open,omitempty" json:"open,omitempty"`             // desktop app IDs; never closed
	Connect      []string `toml:"connect,omitempty" json:"connect,omitempty"`       // NetworkManager connection UUIDs, VPNs included
	Disconnect   []string `toml:"disconnect,omitempty" json:"disconnect,omitempty"` // as Connect
}

func (s Session) empty() bool {
	return s.Wallpaper == "" && s.DoNotDisturb == nil && s.Brightness == nil && s.PowerProfile == "" &&
		len(s.Open) == 0 && len(s.Connect) == 0 && len(s.Disconnect) == 0
}

// StackAction starts or stops a managed app by its name.
type StackAction struct {
	Name string `toml:"name" json:"name"`
	Do   string `toml:"do" json:"do"`
}

// Item is a profile item (a unit, container or Compose project); apps are
// Session.Open. Its JSON is a profiles.Item's.
type Item struct {
	Kind   string `toml:"kind" json:"kind"`
	Target string `toml:"target" json:"target"`
	Do     string `toml:"do" json:"do"`
}

// Trigger switches a mode on by itself once armed by a click. Only the
// fields of its kind may be set.
type Trigger struct {
	Kind       string `toml:"kind" json:"kind"`
	OnCalendar string `toml:"on_calendar,omitempty" json:"on_calendar,omitempty"` // schedule
	Connection string `toml:"connection,omitempty" json:"connection,omitempty"`   // wifi: a connection UUID, never an SSID
	Below      *int   `toml:"below,omitempty" json:"below,omitempty"`             // battery: percent, 5–95
	Event      string `toml:"event,omitempty" json:"event,omitempty"`             // display: connect or disconnect
}

// file is the TOML document.
type file struct {
	Name    string        `toml:"name"`
	Session Session       `toml:"session,omitempty"`
	Stacks  []StackAction `toml:"stack,omitempty"`
	Items   []Item        `toml:"item,omitempty"`
	Trigger []Trigger     `toml:"trigger,omitempty"`
}

// Mode is a valid mode file.
type Mode struct {
	ID       string        `json:"id"`   // the file name without .toml
	Hash     string        `json:"hash"` // sha256 of the file, hex
	Name     string        `json:"name"`
	Session  Session       `json:"session"`
	Stacks   []StackAction `json:"stacks"`
	Items    []Item        `json:"items"`
	Triggers []Trigger     `json:"triggers"`
}

// ActionItems is what a mode asks of the profile actions: its items, its
// stacks, then its apps to open.
func (m Mode) ActionItems() []Item {
	out := make([]Item, 0, len(m.Items)+len(m.Stacks)+len(m.Session.Open))
	out = append(out, m.Items...)
	for _, st := range m.Stacks {
		out = append(out, Item{Kind: KindStack, Target: st.Name, Do: st.Do})
	}
	for _, a := range m.Session.Open {
		out = append(out, Item{Kind: KindApp, Target: a, Do: DoStart})
	}
	return out
}

// HasGame reports whether the mode has a game trigger, whose items must be
// able to run with nobody there (SECURITY S15a).
func (m Mode) HasGame() bool {
	for _, t := range m.Triggers {
		if t.Kind == TriggerGame {
			return true
		}
	}
	return false
}

// ValidID reports whether id can name a mode file.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Hash is the hex sha256 of a file's bytes.
func Hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Parse reads and checks one mode file. id is its file name without .toml.
// Errors are fit to show the user: one line, cleaned and short.
func Parse(id string, raw []byte) (Mode, error) {
	if !ValidID(id) {
		return Mode{}, errors.New("a mode file is named with 1 to 40 lowercase letters, digits, - or _, then .toml")
	}
	if len(raw) > MaxFileSize {
		return Mode{}, fmt.Errorf("the file is larger than %d KiB", MaxFileSize>>10)
	}
	if !utf8.Valid(raw) {
		return Mode{}, errors.New("the file is not UTF-8 text")
	}
	var f file
	md, err := toml.NewDecoder(bytes.NewReader(raw)).Decode(&f)
	if err != nil {
		return Mode{}, errors.New(Clean(tomlError(err)))
	}
	if und := md.Undecoded(); len(und) > 0 {
		return Mode{}, fmt.Errorf("unknown key %q", Clean(und[0].String()))
	}
	m := Mode{ID: id, Hash: Hash(raw), Name: f.Name, Session: f.Session,
		Stacks: nonNil(f.Stacks), Items: nonNil(f.Items), Triggers: nonNil(f.Trigger)}
	if err := m.check(); err != nil {
		return Mode{}, errors.New(Clean(err.Error()))
	}
	return m, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// tomlError is the decoder's message on one line, with its line number.
func tomlError(err error) string {
	var pe toml.ParseError
	if errors.As(err, &pe) {
		msg := pe.Message
		if msg == "" {
			msg = pe.Error()
		}
		return fmt.Sprintf("line %d: %s", pe.Position.Line, msg)
	}
	return err.Error()
}

func (m Mode) check() error {
	if err := checkName(m.Name); err != nil {
		return err
	}
	if m.Session.empty() && len(m.Stacks) == 0 && len(m.Items) == 0 {
		return errors.New("a mode changes at least one thing")
	}
	if err := m.Session.check(); err != nil {
		return err
	}
	if len(m.Stacks) > maxStacks {
		return fmt.Errorf("a mode has at most %d stacks", maxStacks)
	}
	stacks := map[string]bool{}
	for _, s := range m.Stacks {
		if !spec.ValidName(s.Name) {
			return fmt.Errorf("stack %q: not a stack name", s.Name)
		}
		if s.Do != StackStart && s.Do != StackStop {
			return fmt.Errorf("stack %s: do is start or stop", s.Name)
		}
		if stacks[s.Name] {
			return fmt.Errorf("stack %s is listed twice", s.Name)
		}
		stacks[s.Name] = true
	}
	if len(m.Items) > maxItems {
		return fmt.Errorf("a mode has at most %d items", maxItems)
	}
	items := map[[2]string]bool{}
	for _, it := range m.Items {
		switch it.Kind {
		case KindUnit, KindContainer, KindProject:
		case KindApp:
			return errors.New(`apps are opened by session.open, not by an item`)
		case KindStack:
			return errors.New(`stacks are listed as [[stack]], not as an item`)
		default:
			return fmt.Errorf("item %q: kind is unit, container or project", it.Kind)
		}
		if err := checkTarget(it.Target); err != nil {
			return fmt.Errorf("%s item: %w", it.Kind, err)
		}
		if it.Do != DoStart && it.Do != DoStop {
			return fmt.Errorf("%s: do is start or stop", it.Target)
		}
		key := [2]string{it.Kind, it.Target}
		if items[key] {
			return fmt.Errorf("%s is listed twice", it.Target)
		}
		items[key] = true
	}
	if len(m.Triggers) > maxTriggers {
		return fmt.Errorf("a mode has at most %d triggers", maxTriggers)
	}
	for i, t := range m.Triggers {
		if err := t.check(); err != nil {
			return fmt.Errorf("trigger %d: %w", i+1, err)
		}
	}
	return nil
}

func checkName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return fmt.Errorf("name is 1 to %d characters", maxName)
	}
	if strings.TrimSpace(name) != name {
		return errors.New("name does not start or end with a space")
	}
	for _, r := range name {
		if !unicode.IsGraphic(r) {
			return errors.New("name is one line of visible characters")
		}
	}
	return nil
}

// checkTarget checks a name's shape; what it names is checked by the action
// that would act on it.
func checkTarget(s string) error {
	if s == "" || len(s) > maxTarget {
		return fmt.Errorf("a target is 1 to %d bytes", maxTarget)
	}
	for _, r := range s {
		if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
			return errors.New("a target is one word of visible characters")
		}
	}
	return nil
}

func (s Session) check() error {
	if s.Wallpaper != "" {
		if err := session.CheckWallpaper(s.Wallpaper); err != nil {
			return err
		}
	}
	if s.Brightness != nil && (*s.Brightness < 1 || *s.Brightness > 100) {
		return errors.New("brightness is 1 to 100")
	}
	if s.PowerProfile != "" && !powerProfiles[s.PowerProfile] {
		return errors.New("power_profile is power-saver, balanced or performance")
	}
	if len(s.Open) > maxOpen {
		return fmt.Errorf("session.open lists at most %d apps", maxOpen)
	}
	seen := map[string]bool{}
	for _, a := range s.Open {
		if err := checkTarget(a); err != nil {
			return fmt.Errorf("session.open: %w", err)
		}
		if seen[a] {
			return fmt.Errorf("session.open lists %s twice", a)
		}
		seen[a] = true
	}
	conns := map[string]string{}
	for _, l := range []struct {
		key  string
		list []string
	}{{"connect", s.Connect}, {"disconnect", s.Disconnect}} {
		if len(l.list) > maxConnect {
			return fmt.Errorf("session.%s lists at most %d connections", l.key, maxConnect)
		}
		for _, u := range l.list {
			if !uuidPattern.MatchString(u) {
				return fmt.Errorf("session.%s: %q is not a connection UUID", l.key, u)
			}
			if prev, ok := conns[u]; ok {
				if prev == l.key {
					return fmt.Errorf("session.%s lists %s twice", l.key, u)
				}
				return fmt.Errorf("%s is in both connect and disconnect", u)
			}
			conns[u] = l.key
		}
	}
	return nil
}

func (t Trigger) check() error {
	// Only the fields of its kind may be set, so nothing is silently ignored.
	set := map[string]bool{"on_calendar": t.OnCalendar != "", "connection": t.Connection != "", "below": t.Below != nil, "event": t.Event != ""}
	own := ""
	switch t.Kind {
	case TriggerSchedule:
		own = "on_calendar"
		if err := safeexec.CalendarExpr(t.OnCalendar); err != nil {
			return fmt.Errorf("on_calendar: %w", err)
		}
	case TriggerWiFi:
		own = "connection"
		if !uuidPattern.MatchString(t.Connection) {
			return errors.New("connection is a NetworkManager connection UUID; an SSID can be faked by anyone")
		}
	case TriggerBattery:
		own = "below"
		if t.Below == nil || *t.Below < 5 || *t.Below > 95 {
			return errors.New("below is 5 to 95")
		}
	case TriggerDisplay:
		own = "event"
		if t.Event != "connect" && t.Event != "disconnect" {
			return errors.New("event is connect or disconnect")
		}
	case TriggerGame:
	default:
		return errors.New("kind is schedule, wifi, battery, display or game")
	}
	for _, k := range []string{"on_calendar", "connection", "below", "event"} {
		if set[k] && k != own {
			return fmt.Errorf("a %s trigger has no %s", t.Kind, k)
		}
	}
	return nil
}

// Clean makes s fit to show: control and other invisible characters become
// U+FFFD, and it is cut to 300 characters.
func Clean(s string) string {
	const limit = 300
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == limit {
			b.WriteString("…")
			break
		}
		if r != ' ' && !unicode.IsGraphic(r) {
			r = utf8.RuneError
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
