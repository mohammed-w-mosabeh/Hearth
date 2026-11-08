package modes

import "strconv"

// Step is one registered action a mode's session settings take, as the
// actions API names it: "kind/name", a target and params.
type Step struct {
	Action string            `json:"action"`
	Target string            `json:"target"`
	Params map[string]string `json:"params"`
}

// SessionSteps are the actions that apply m's session settings (ROADMAP
// 10.3), each validated and audited by the registry like the same click
// elsewhere. Apps to open are profile items (ActionItems), never closed.
// backlight is the one brightness applies to (session.PreferredBacklight);
// with none, brightness is left out. Connections go down before others come
// up, so a VPN that replaces another does not overlap it.
func (m Mode) SessionSteps(backlight string) []Step {
	s := m.Session
	out := []Step{}
	add := func(action, target string, params map[string]string) {
		if params == nil {
			params = map[string]string{}
		}
		out = append(out, Step{Action: action, Target: target, Params: params})
	}
	if s.PowerProfile != "" {
		add("power/profile", s.PowerProfile, nil)
	}
	if s.Brightness != nil && backlight != "" {
		add("system/brightness", backlight, map[string]string{"percent": strconv.Itoa(*s.Brightness)})
	}
	if s.DoNotDisturb != nil {
		v := "off"
		if *s.DoNotDisturb {
			v = "on"
		}
		add("session/dnd", v, nil)
	}
	if s.Wallpaper != "" {
		add("session/wallpaper", s.Wallpaper, nil)
	}
	for _, u := range s.Disconnect {
		add("network/disconnect", u, nil)
	}
	for _, u := range s.Connect {
		add("network/connect", u, nil)
	}
	return out
}
