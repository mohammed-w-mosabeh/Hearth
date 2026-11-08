package modes

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/session"
)

// Names of the session settings a mode sets, as setting items name them.
const (
	SettingPower      = "power-profile"
	SettingBrightness = "brightness"
	SettingDND        = "dnd"
	SettingWallpaper  = "wallpaper"
)

// Item kinds for the session (ROADMAP 10.5).
const (
	KindSetting    = "setting"    // Target a Setting* name, Do the value
	KindConnection = "connection" // Target a connection UUID, Do start or stop
)

// sessionItems are the items of m's session settings: power and brightness
// first, then Do Not Disturb and the wallpaper; connections go down before
// others come up, so a VPN that replaces another does not overlap it.
func (m Mode) sessionItems() []Item {
	s := m.Session
	out := []Item{}
	set := func(name, value string) { out = append(out, Item{Kind: KindSetting, Target: name, Do: value}) }
	if s.PowerProfile != "" {
		set(SettingPower, s.PowerProfile)
	}
	if s.Brightness != nil {
		set(SettingBrightness, strconv.Itoa(*s.Brightness))
	}
	if s.DoNotDisturb != nil {
		set(SettingDND, onOff(*s.DoNotDisturb))
	}
	if s.Wallpaper != "" {
		set(SettingWallpaper, s.Wallpaper)
	}
	for _, u := range s.Disconnect {
		out = append(out, Item{Kind: KindConnection, Target: u, Do: DoStop})
	}
	for _, u := range s.Connect {
		out = append(out, Item{Kind: KindConnection, Target: u, Do: DoStart})
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Step is one registered action, as the actions API names it: "kind/name",
// a target and params.
type Step struct {
	Action string            `json:"action"`
	Target string            `json:"target"`
	Params map[string]string `json:"params"`
}

// StepFor is the registered action that does a setting or connection item.
// A setting's value is either what a mode file holds or what Current read,
// to put it back. backlight is the one brightness applies to.
func StepFor(it Item, backlight string) (Step, error) {
	st := Step{Target: it.Target, Params: map[string]string{}}
	switch it.Kind {
	case KindConnection:
		st.Action = "network/disconnect"
		if it.Do == DoStart {
			st.Action = "network/connect"
		}
		return st, nil
	case KindSetting:
	default:
		return st, fmt.Errorf("modes: %s is not a session item", it.Kind)
	}
	st.Target = it.Do
	switch it.Target {
	case SettingPower:
		st.Action = "power/profile"
	case SettingDND:
		st.Action = "session/dnd"
	case SettingBrightness:
		if backlight == "" {
			return st, errors.New("modes: no backlight")
		}
		n, err := strconv.Atoi(it.Do)
		if err != nil {
			return st, errors.New("modes: brightness is a percent")
		}
		st.Action, st.Target, st.Params["percent"] = "system/brightness", backlight, strconv.Itoa(min(max(n, 1), 100))
	case SettingWallpaper:
		// A value Current read is both URIs; a mode file's is a path.
		if uri, dark, ok := strings.Cut(it.Do, "|"); ok {
			st.Action, st.Target = "session/wallpaper-uris", "wallpaper"
			st.Params["uri"], st.Params["dark_uri"] = uri, dark
		} else {
			st.Action = "session/wallpaper"
		}
	default:
		return st, fmt.Errorf("modes: no setting %q", it.Target)
	}
	return st, nil
}

// SessionSteps are the actions that apply m's session settings (ROADMAP
// 10.3), each validated and audited by the registry. Apps to open are
// profile items, never closed. With no backlight, brightness is left out.
func (m Mode) SessionSteps(backlight string) []Step {
	out := []Step{}
	for _, it := range m.sessionItems() {
		if st, err := StepFor(it, backlight); err == nil {
			out = append(out, st)
		}
	}
	return out
}

// SessionSettings reads and sets session settings for the profile engine
// (profiles.Settings).
type SessionSettings struct {
	Session *session.Session
	// Power is the active power profile, false when the daemon is absent.
	Power func() (string, bool)
}

// Current is a setting's value now, in the form StepFor takes. A value that
// could not be put back exactly is an error, so the setting is not changed.
func (s SessionSettings) Current(ctx context.Context, name string) (string, error) {
	switch name {
	case SettingPower:
		if s.Power != nil {
			if p, ok := s.Power(); ok && p != "" {
				return p, nil
			}
		}
		return "", errors.New("modes: no power profile")
	case SettingBrightness:
		b, ok := s.Session.PreferredBacklight()
		if !ok {
			return "", errors.New("modes: no backlight")
		}
		return strconv.Itoa(b.Percent), nil
	case SettingDND:
		if d := s.Session.Current(ctx).DoNotDisturb; d != nil {
			return onOff(*d), nil
		}
		return "", errors.New("modes: Do Not Disturb cannot be read")
	case SettingWallpaper:
		w := s.Session.Current(ctx).Wallpaper
		if w == nil || safeexec.GSettingsFileURI(w.URI) != nil || safeexec.GSettingsFileURI(w.DarkURI) != nil {
			return "", errors.New("modes: the wallpaper cannot be read, or not put back")
		}
		return w.URI + "|" + w.DarkURI, nil
	}
	return "", fmt.Errorf("modes: no setting %q", name)
}

// Request is the action and request that set name to value.
func (s SessionSettings) Request(name, value string) (string, actions.Request, error) {
	backlight := ""
	if name == SettingBrightness {
		if b, ok := s.Session.PreferredBacklight(); ok {
			backlight = b.Name
		}
	}
	st, err := StepFor(Item{Kind: KindSetting, Target: name, Do: value}, backlight)
	if err != nil {
		return "", actions.Request{}, err
	}
	return st.Action, actions.Request{Target: st.Target, Params: st.Params}, nil
}
