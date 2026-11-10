package automations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
)

type sent struct {
	mu   sync.Mutex
	list []string // "channel|summary|body"
	fail map[string]bool
}

func (s *sent) steps() Steps {
	rec := func(ch, summary, body string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.fail[ch] {
			return errors.New("down")
		}
		s.list = append(s.list, ch+"|"+summary+"|"+body)
		return nil
	}
	return Steps{
		Desktop: func(_ context.Context, summary, body string) error { return rec("desktop", summary, body) },
		Send:    func(_ context.Context, ch, summary, body string) error { return rec(ch, summary, body) },
		Top: func(context.Context) ([]string, error) {
			return []string{"firefox 40%", "code 20%", "gnome-shell 5%", "four 1%"}, nil
		},
		Journal: func(_ context.Context, unit string, lines int) ([]string, error) {
			if unit != "system/web.service" || lines != journalLines {
				return nil, errors.New("wrong unit")
			}
			return []string{"started", "DB_PASSWORD=hunter2", "url https://bob:pw@example.org/x", "bell\x07here"}, nil
		},
		State: State{
			ContainerState: func(_ context.Context, name string) (string, error) {
				if name != "web" {
					return "", errors.New("no container")
				}
				return "running unhealthy", nil
			},
			UnitState: func(_ context.Context, scope, name string) (string, error) {
				return map[string]string{"system/a.service": "active", "user/b.service": "failed"}[scope+"/"+name], nil
			},
		},
	}
}

func TestNotifyFillsCleansAndMasks(t *testing.T) {
	s := &sent{}
	st := spec.Step{Step: "notify", Channels: []string{"desktop", "mail"},
		Text: "{automation}: {trigger} on {instance} = {value}. Top: {top}. Log:{journal} {unknown}"}
	ev := Event{Kind: "unit_failed", Instance: "system/web.service", Value: 1.5}
	if err := s.steps().Notify(context.Background(), st, ev, "Watch\x1b[31m web"); err != nil {
		t.Fatal(err)
	}
	if len(s.list) != 2 || !strings.HasPrefix(s.list[0], "desktop|") || !strings.HasPrefix(s.list[1], "smtp|") {
		t.Fatalf("mail goes through smtp, each channel once: %q", s.list)
	}
	body := strings.SplitN(s.list[0], "|", 3)[2]
	for _, want := range []string{"unit failed on system/web.service = 1.5", "firefox 40%, code 20%, gnome-shell 5%.", "started", hidden, "bell here", "{unknown}"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"hunter2", "bob:pw", "four", "\x07", "\x1b"} {
		if strings.Contains(s.list[0], bad) {
			t.Errorf("notification holds %q:\n%s", bad, s.list[0])
		}
	}
}

func TestNotifyPlaceholdersOnlyWhereTheyApply(t *testing.T) {
	s := &sent{}
	st := spec.Step{Step: "notify", Channels: []string{"ntfy"}, Text: "[{journal}][{sources}][{outage}]"}
	if err := s.steps().Notify(context.Background(), st, Event{Kind: "container", Instance: "web", Detail: "1.2.3.4"}, "n"); err != nil {
		t.Fatal(err)
	}
	if got := strings.SplitN(s.list[0], "|", 3)[2]; got != "[][][]" {
		t.Fatalf("got %q", got)
	}
	s.list = nil
	if err := s.steps().Notify(context.Background(), st, Event{Kind: "ssh_failed", Detail: "1.2.3.4, host", Outage: 90}, "n"); err != nil {
		t.Fatal(err)
	}
	if got := strings.SplitN(s.list[0], "|", 3)[2]; got != "[][1.2.3.4, host][1m30s]" {
		t.Fatalf("got %q", got)
	}
}

func TestNotifyOneChannelFailingDoesNotStopTheOthers(t *testing.T) {
	s := &sent{fail: map[string]bool{"desktop": true}}
	err := s.steps().Notify(context.Background(), spec.Step{Channels: []string{"desktop", "ntfy"}, Text: "x"}, Event{}, "n")
	if err == nil || !strings.Contains(err.Error(), "desktop") || len(s.list) != 1 {
		t.Fatalf("%v %q", err, s.list)
	}
	if err := (Steps{}).Notify(context.Background(), spec.Step{Channels: []string{"mail"}, Text: "x"}, Event{}, "n"); err == nil {
		t.Fatal("a missing channel must fail")
	}
}

func TestMaskBounds(t *testing.T) {
	m := Mask(strings.Repeat("é", maxNotice))
	if len(m) > maxNotice+len("…") || !strings.HasSuffix(m, "…") {
		t.Fatalf("len %d", len(m))
	}
	if got := Mask("ok\napi_key: abc\nfine"); got != "ok\n"+hidden+"\nfine" {
		t.Fatalf("%q", got)
	}
}

func TestCheckStep(t *testing.T) {
	st := (&sent{}).steps()
	cases := []struct {
		step spec.Step
		want bool
		err  bool
	}{
		{spec.Step{What: "container_health", Target: "web", Expect: "unhealthy"}, true, false},
		{spec.Step{What: "container_health", Target: "web", Expect: "healthy"}, false, false},
		{spec.Step{What: "container_state", Target: "web", Expect: "running"}, true, false},
		{spec.Step{What: "container_state", Target: "db", Expect: "running"}, false, true},
		{spec.Step{What: "unit_state", Target: "a.service", Expect: "active"}, true, false},
		{spec.Step{What: "unit_state", Target: "system/a.service", Expect: "active"}, true, false},
		{spec.Step{What: "unit_state", Target: "user/b.service", Expect: "failed"}, true, false},
		{spec.Step{What: "unit_state", Target: "user/b.service", Expect: "active"}, false, false},
	}
	for _, c := range cases {
		got, err := st.Check(context.Background(), c.step)
		if got != c.want || (err != nil) != c.err {
			t.Errorf("%+v: %v %v", c.step, got, err)
		}
	}
	if _, err := (Steps{}).Check(context.Background(), spec.Step{What: "unit_state", Target: "a"}); err == nil {
		t.Error("no source must fail")
	}
}

func TestEngineRunsNotifyAndCheck(t *testing.T) {
	g := newEngineRig(t)
	s := &sent{}
	g.e.cfg.Steps = s.steps()
	g.add("heal", `{"trigger":{"kind":"usb_device"},"then":[
		{"step":"check","what":"container_health","target":"web","expect":"unhealthy",
		 "then":[{"step":"action","action":"unit/restart","target":"user/web.service"},{"step":"notify","channels":["desktop"],"text":"restarted {instance}"}],
		 "else":[{"step":"notify","channels":["desktop"],"text":"fine"}]}]}`)
	g.observe("usb.new_device", map[string]float64{"1-2": 1})
	if r := g.ranList(); r != "unit/restart user/web.service" {
		t.Fatalf("ran %q", r)
	}
	if len(s.list) != 1 || s.list[0] != "desktop|heal|restarted 1-2" {
		t.Fatalf("%q", s.list)
	}
	if st := g.st.states(); len(st) != 1 || !strings.HasPrefix(st[0], "done") {
		t.Fatalf("%v", st)
	}
}

// protectedRig registers actions whose plans name a protected target or
// need a typed confirmation, as unit and package actions do.
func protectedRig(t *testing.T) *engineRig {
	g := newEngineRig(t)
	plan := func(p actions.Plan) func(context.Context, actions.Request) (actions.Plan, error) {
		return func(context.Context, actions.Request) (actions.Plan, error) { return p, nil }
	}
	run := func(context.Context, actions.Request, actions.Plan) (actions.Result, error) {
		t.Error("a protected step ran")
		return actions.Result{}, nil
	}
	ok := func(actions.Request) error { return nil }
	g.reg.Register(actions.Action{Kind: "unit", Name: "stop", Risk: actions.Reversible, Validate: ok, Run: run,
		Plan: plan(actions.Plan{Risk: actions.Destructive, Confirm: "gdm.service"})})
	g.reg.Register(actions.Action{Kind: "package", Name: "remove", Risk: actions.Destructive, Validate: ok, Run: run,
		Plan: plan(actions.Plan{Blocked: []actions.Blocked{{Item: actions.Item{ID: "libc6", Name: "libc6"}, Reason: "essential"}}})})
	return g
}

func lastSteps(t *testing.T, g *engineRig) []StepResult {
	t.Helper()
	var rec RunRecord
	if err := json.Unmarshal(g.st.runs[len(g.st.runs)-1].Record, &rec); err != nil {
		t.Fatal(err)
	}
	return rec.Steps
}

func TestProtectedTargetsRefusedAtRunNotWaiting(t *testing.T) {
	for _, step := range []string{
		`{"step":"action","action":"unit/stop","target":"system/gdm.service"}`,
		`{"step":"action","action":"package/remove","target":"libc6"}`,
		`{"step":"action","action":"package/remove","target":"linux-image-6.12.1-amd64"}`,
	} {
		g := protectedRig(t)
		g.e.cfg.Protect = func(action, target string) string {
			if strings.HasPrefix(target, "linux-image") {
				return "the running kernel"
			}
			return ""
		}
		g.add("p", `{"trigger":{"kind":"usb_device"},"then":[`+step+`,{"step":"action","action":"power/profile","target":"after"}]}`)
		g.observe("usb.new_device", map[string]float64{"1": 1})
		if st := g.st.states(); len(st) != 1 || !strings.HasPrefix(st[0], "failed") {
			t.Fatalf("%s: %v", step, st)
		}
		steps := lastSteps(t, g)
		if len(steps) != 1 || !strings.HasPrefix(steps[0].Detail, "refused: ") {
			t.Fatalf("%s: %+v", step, steps)
		}
		if g.ranList() != "" {
			t.Fatalf("%s: ran %q", step, g.ranList())
		}
	}
}

func TestCheckerRefusesAtSave(t *testing.T) {
	g := protectedRig(t)
	g.reg.Register(actions.Action{Kind: "svc", Name: "down", Risk: actions.Safe, Authority: actions.AuthorityNone,
		Validate: func(actions.Request) error { return nil },
		Plan: func(context.Context, actions.Request) (actions.Plan, error) {
			return actions.Plan{}, actions.Errorf(http.StatusServiceUnavailable, "the service is down")
		},
		Run: func(context.Context, actions.Request, actions.Plan) (actions.Result, error) {
			return actions.Result{}, nil
		}})
	g.reg.Register(actions.Action{Kind: "svc", Name: "picky", Risk: actions.Safe, Authority: actions.AuthorityNone,
		Validate: func(r actions.Request) error {
			if r.Target != "ok" {
				return errors.New("target must be ok")
			}
			return nil
		},
		Plan: func(ctx context.Context, _ actions.Request) (actions.Plan, error) {
			if !unattended.Is(ctx) {
				t.Error("a save preview ran without the unattended mark")
			}
			return actions.Plan{}, nil
		},
		Run: func(context.Context, actions.Request, actions.Plan) (actions.Result, error) {
			return actions.Result{}, nil
		}})
	check := NewChecker(CheckDeps{Registry: g.reg, Protect: func(_, target string) string {
		if target == "linux-image-new" {
			return "one of the two newest kernels"
		}
		return ""
	}})
	try := func(step string) error {
		s, err := spec.Parse([]byte(`{"trigger":{"kind":"usb_device"},"then":[` + step + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		return check(context.Background(), s)
	}
	refused := map[string]string{
		`{"step":"action","action":"unit/stop","target":"system/gdm.service"}`:                                                                              "typed confirmation",
		`{"step":"action","action":"package/remove","target":"libc6"}`:                                                                                      "protected (essential)",
		`{"step":"action","action":"package/remove","target":"linux-image-new"}`:                                                                            "two newest",
		`{"step":"action","action":"no/such","target":"x"}`:                                                                                                 "not an action",
		`{"step":"action","action":"svc/picky","target":"bad"}`:                                                                                             "target must be ok",
		`{"step":"action","action":"unit/restart","target":"user/a.service","continue":true}`:                                                               "continue",
		`{"step":"action","action":"container/restart","target":"web","continue":true}`:                                                                     "continue",
		`{"step":"check","what":"unit_state","target":"a","expect":"active","then":[{"step":"action","action":"unit/stop","target":"system/gdm.service"}]}`: "typed confirmation",
	}
	for step, want := range refused {
		if err := try(step); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", step, err, want)
		}
	}
	for _, step := range []string{
		`{"step":"action","action":"svc/picky","target":"ok"}`,
		`{"step":"action","action":"svc/down","target":"x"}`, // checked again at run
		`{"step":"action","action":"x/flaky","target":"1","continue":true}`,
		`{"step":"action","action":"image/remove","target":"old"}`, // destructive: waits for approval, not refused
	} {
		if err := try(step); err != nil {
			t.Errorf("%s: %v", step, err)
		}
	}
}

func TestNotifySendAction(t *testing.T) {
	s := &sent{}
	reg := actions.NewRegistry(&memStore{})
	s.steps().RegisterActions(reg)
	if reg.AuthorityOf("notify/send", actions.Request{}) != actions.AuthorityNone {
		t.Fatal("notify/send never asks for a password")
	}
	for _, r := range []actions.Request{
		{Target: "desktop", Params: map[string]string{}},
		{Target: "pager", Params: map[string]string{"text": "x"}},
		{Target: "desktop,desktop", Params: map[string]string{"text": "x"}},
		{Target: "desktop", Params: map[string]string{"text": "{automation}"}},
		{Target: "desktop", Params: map[string]string{"text": "x", "cmd": "y"}},
		{Target: "desktop", Params: map[string]string{"text": "x", "title": "a\nb"}},
	} {
		if _, _, err := reg.PlanFor(context.Background(), "notify/send", r); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	u, err := reg.RunUnattended(context.Background(), "notify/send",
		actions.Request{Target: "desktop,ntfy", Params: map[string]string{"text": "token=abc", "title": "T"}}, "automation:1")
	if err != nil || u.Result.Done != 1 {
		t.Fatalf("%v %+v", err, u)
	}
	if len(s.list) != 2 || s.list[0] != "desktop|T|"+hidden {
		t.Fatalf("%q", s.list)
	}
}

func TestTopTimesOutWithContext(t *testing.T) {
	// {top} gives "unknown" rather than holding a notification up.
	s := Steps{Top: func(ctx context.Context) ([]string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if got := s.fill(ctx, "{top}", Event{}, "n"); got != "unknown" {
		t.Fatalf("%q", got)
	}
}
