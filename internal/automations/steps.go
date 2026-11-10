package automations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/secret"
)

// The notify and check steps (ROADMAP 12.5, docs/plan/automations.md §4).

// Steps runs notify and check steps; it is the engine's StepRunner.
type Steps struct {
	// Desktop shows a desktop notification; Send sends through a remote
	// channel ("ntfy" or "smtp"). Either may be nil (that channel fails).
	Desktop func(ctx context.Context, summary, body string) error
	Send    func(ctx context.Context, channel, summary, body string) error
	// Top are the busiest processes by CPU, as "name 12%" (the {top}
	// placeholder); Journal the last lines of a unit, "scope/unit" ({journal}).
	Top     func(ctx context.Context) ([]string, error)
	Journal func(ctx context.Context, unit string, lines int) ([]string, error)
	// State answers check steps (the engine's own).
	State State
}

// Bounds of a notification.
const (
	maxNotice    = 2000
	journalLines = 10
	topCount     = 3
	hidden       = "[hidden]"
)

// Notify fills the step's placeholders from the event, cleans and masks
// the text (S19), and sends it to each of the step's channels. A channel
// that fails does not stop the others; the first error is returned.
func (s Steps) Notify(ctx context.Context, st spec.Step, ev Event, name string) error {
	body := Mask(s.fill(ctx, st.Text, ev, name))
	return s.send(ctx, st.Channels, Mask(oneLine(name)), body)
}

// SendText sends text as given (cleaned and masked) to channels: the
// notify/send action.
func (s Steps) SendText(ctx context.Context, channels []string, summary, text string) error {
	return s.send(ctx, channels, Mask(oneLine(summary)), Mask(text))
}

func (s Steps) send(ctx context.Context, channels []string, summary, body string) error {
	var first error
	for _, c := range channels {
		var err error
		switch c {
		case "desktop":
			if s.Desktop == nil {
				err = errors.New("desktop notifications are not available")
			} else {
				err = s.Desktop(ctx, summary, body)
			}
		case "ntfy", "mail":
			ch := map[string]string{"ntfy": "ntfy", "mail": "smtp"}[c]
			if s.Send == nil {
				err = fmt.Errorf("%s is not available", c)
			} else {
				err = s.Send(ctx, ch, summary, body)
			}
		default:
			err = fmt.Errorf("unknown channel %q", c)
		}
		if err != nil && first == nil {
			first = fmt.Errorf("%s: %w", c, err)
		}
	}
	return first
}

// fill replaces the placeholders the text holds; each value is read only
// when the text asks for it.
func (s Steps) fill(ctx context.Context, text string, ev Event, name string) string {
	values := map[string]func() string{
		"automation": func() string { return oneLine(name) },
		"trigger":    func() string { return strings.ReplaceAll(ev.Kind, "_", " ") },
		"instance":   func() string { return oneLine(ev.Instance) },
		"value": func() string {
			if ev.Value == 0 {
				return ""
			}
			return strconv.FormatFloat(ev.Value, 'f', -1, 64)
		},
		"outage": func() string {
			if ev.Outage <= 0 {
				return ""
			}
			return (time.Duration(ev.Outage) * time.Second).String()
		},
		"sources": func() string {
			if ev.Kind != "ssh_failed" {
				return ""
			}
			return oneLine(ev.Detail)
		},
		"top": func() string {
			if s.Top == nil {
				return "unknown"
			}
			top, err := s.Top(ctx)
			if err != nil {
				return "unknown"
			}
			return strings.Join(top[:min(len(top), topCount)], ", ")
		},
		"journal": func() string {
			scope, _, ok := strings.Cut(ev.Instance, "/")
			if s.Journal == nil || !ok || (scope != "system" && scope != "user") {
				return ""
			}
			lines, err := s.Journal(ctx, ev.Instance, journalLines)
			if err != nil {
				return "the journal could not be read"
			}
			return "\n" + strings.Join(lines, "\n")
		},
	}
	var b strings.Builder
	rest := text
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			b.WriteString(rest)
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		key := rest[i+1 : i+j]
		if f, ok := values[key]; ok && slices.Contains(spec.Placeholders, key) {
			b.WriteString(f())
		} else {
			b.WriteString(rest[i : i+j+1]) // the validator refuses these; kept as text
		}
		rest = rest[i+j+1:]
	}
	return b.String()
}

// Mask cleans text for a notification: control characters other than a
// newline become spaces, and every line that names a secret or holds a
// URL with user info is replaced (S19). It is bounded.
func Mask(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		l = oneLine(l)
		if secret.Line(l) {
			l = hidden
		}
		lines[i] = l
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxNotice {
		out = strings.ToValidUTF8(out[:maxNotice], "") + "…"
	}
	return out
}

// oneLine replaces control characters with spaces.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
}

// Check evaluates a check step: a container's health or state, or a unit's
// state ("system/<unit>" or "user/<unit>"; a bare name is a system unit).
func (s Steps) Check(ctx context.Context, st spec.Step) (bool, error) {
	switch st.What {
	case "container_health", "container_state":
		if s.State.ContainerState == nil {
			return false, errors.New("no container source")
		}
		got, err := s.State.ContainerState(ctx, st.Target)
		if err != nil {
			return false, errors.New("the container's state could not be read")
		}
		return slices.Contains(strings.Fields(got), st.Expect), nil
	case "unit_state":
		if s.State.UnitState == nil {
			return false, errors.New("no unit source")
		}
		scope, unit := "system", st.Target
		if sc, u, ok := strings.Cut(st.Target, "/"); ok && (sc == "system" || sc == "user") {
			scope, unit = sc, u
		}
		got, err := s.State.UnitState(ctx, scope, unit)
		if err != nil {
			return false, errors.New("the unit's state could not be read")
		}
		return got == st.Expect, nil
	}
	return false, fmt.Errorf("unknown check %q", st.What)
}

// RegisterActions adds notify/send (safe, never asks for a password): the
// text as given, cleaned and masked, to the channels the target lists
// ("desktop", "desktop,ntfy", ...), so the UI and a test can send what a
// notify step would.
func (s Steps) RegisterActions(reg *actions.Registry) {
	reg.Register(actions.Action{Kind: "notify", Name: "send", Risk: actions.Safe, Authority: actions.AuthorityNone,
		Validate: func(r actions.Request) error {
			chans := strings.Split(r.Target, ",")
			st := spec.Step{Step: "notify", Channels: chans, Text: r.Params["text"]}
			for k := range r.Params {
				if k != "text" && k != "title" {
					return fmt.Errorf("unknown param %q", k)
				}
			}
			if t := r.Params["title"]; utf8.RuneCountInString(t) > spec.MaxName || oneLine(t) != t {
				return fmt.Errorf("title must be %d characters at most", spec.MaxName)
			}
			if strings.ContainsAny(st.Text, "{}") {
				return errors.New("text holds no placeholders here")
			}
			return spec.CheckNotify(st)
		},
		Plan: func(_ context.Context, r actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: "send a notification", Items: []actions.Item{{ID: r.Target, Name: r.Target}}, Blocked: []actions.Blocked{}}, nil
		},
		Run: func(ctx context.Context, r actions.Request, _ actions.Plan) (actions.Result, error) {
			title := r.Params["title"]
			if title == "" {
				title = "Hearth"
			}
			if err := s.SendText(ctx, strings.Split(r.Target, ","), title, r.Params["text"]); err != nil {
				return actions.Result{Failed: []actions.Blocked{{Item: actions.Item{ID: r.Target, Name: r.Target, Detail: oneLine(err.Error())}, Reason: "error"}}}, nil
			}
			return actions.Result{Done: 1, Failed: []actions.Blocked{}}, nil
		}})
}
