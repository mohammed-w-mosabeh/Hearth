package automations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const parkSpec = `{"trigger":{"kind":"usb_device"},"then":[
	{"step":"action","action":"power/profile","target":"balanced"},
	{"step":"action","action":"container/restart","target":"web"},
	{"step":"notify","channels":["desktop"],"text":"restarted on {instance}"},
	{"step":"action","action":"power/profile","target":"power-saver"}]}`

func approvalRig(t *testing.T) (*engineRig, *sent) {
	t.Helper()
	g := newEngineRig(t)
	s := &sent{}
	g.e.cfg.Steps = s.steps()
	g.e.RegisterApprovals(g.reg)
	return g, s
}

// do previews and runs an approval action as the actions API would, with
// the user there.
func approve(t *testing.T, g *engineRig, key, id string) (actions.Result, error) {
	t.Helper()
	a, ok := g.reg.Lookup(key)
	if !ok {
		t.Fatal(key)
	}
	r := actions.Request{Target: id, Params: map[string]string{}}
	if err := a.Validate(r); err != nil {
		return actions.Result{}, err
	}
	p, err := a.Plan(context.Background(), r)
	if err != nil {
		return actions.Result{}, err
	}
	res, err := a.Run(context.Background(), r, p)
	g.e.Wait()
	return res, err
}

func runRecord(t *testing.T, g *engineRig, i int) (string, RunRecord) {
	t.Helper()
	g.st.mu.Lock()
	r := g.st.runs[i]
	g.st.mu.Unlock()
	var rec RunRecord
	if err := json.Unmarshal(r.Record, &rec); err != nil {
		t.Fatal(err)
	}
	return r.State, rec
}

func TestRunParksAndNotifies(t *testing.T) {
	g, _ := approvalRig(t)
	g.add("heal", parkSpec)
	g.observe("usb.new_device", map[string]float64{"1-2": 1})
	if r := g.ranList(); r != "power/profile balanced" {
		t.Fatalf("ran %q", r)
	}
	if len(g.st.approvs) != 1 {
		t.Fatalf("%+v", g.st.approvs)
	}
	ap := g.st.approvs[0]
	var p Parked
	if err := json.Unmarshal(ap.Steps, &p); err != nil || len(p.Steps) != 3 || p.Steps[0].Action != "container/restart" || p.Trigger.Instance != "1-2" {
		t.Fatalf("%v %+v", err, p)
	}
	if ap.Expires-ap.Created != ApprovalTTL.Milliseconds() || ap.AutomationID != 1 {
		t.Fatalf("%+v", ap)
	}
	if len(g.notes) != 1 || g.notes[0] != "Waiting for your approval" {
		t.Fatalf("%q", g.notes)
	}
	views, err := g.e.Approvals(context.Background())
	if err != nil || len(views) != 1 || views[0].Name != "heal" || len(views[0].Steps) != 3 {
		t.Fatalf("%v %+v", err, views)
	}
}

func TestApproveRunsTheRestAttended(t *testing.T) {
	g, s := approvalRig(t)
	g.add("heal", parkSpec)
	g.observe("usb.new_device", map[string]float64{"1-2": 1})
	a, _ := g.reg.Lookup("automation/approve")
	p, err := a.Plan(context.Background(), actions.Request{Target: "1"})
	if err != nil || len(p.Items) != 3 || p.Items[0].ID != "1" || p.Items[0].Name != "container/restart web" || !strings.Contains(p.Items[1].Detail, "desktop") {
		t.Fatalf("each parked step is previewed: %v %+v", err, p.Items)
	}
	res, err := approve(t, g, "automation/approve", "1")
	if err != nil || res.Done != 3 {
		t.Fatalf("%v %+v", err, res)
	}
	if r := g.ranList(); r != "power/profile balanced,container/restart web (attended),power/profile power-saver (attended)" {
		t.Fatalf("ran %q", r)
	}
	if len(s.list) != 1 || s.list[0] != "desktop|heal|restarted on 1-2" {
		t.Fatalf("the trigger's event fills the notification: %q", s.list)
	}
	state, rec := runRecord(t, g, 0)
	if state != RunDone || len(rec.Steps) != 4 {
		t.Fatalf("%s %+v", state, rec.Steps)
	}
	for _, st := range rec.Steps {
		if st.Result != "ok" {
			t.Fatalf("%+v", rec.Steps)
		}
	}
	if _, err := approve(t, g, "automation/approve", "1"); err == nil || !strings.Contains(err.Error(), "already approved") {
		t.Fatalf("a second click must not run it again: %v", err)
	}
	var audited bool
	for _, e := range g.st.audit {
		if e.Action == "container.restart" && e.Origin == "automation:1" {
			audited = true
		}
	}
	if !audited {
		t.Fatal("an approved step is audited as the automation's")
	}
}

func TestDismissAndExpiry(t *testing.T) {
	g, _ := approvalRig(t)
	g.add("heal", parkSpec)
	g.observe("usb.new_device", map[string]float64{"1-2": 1})
	if _, err := approve(t, g, "automation/dismiss", "1"); err != nil {
		t.Fatal(err)
	}
	state, rec := runRecord(t, g, 0)
	if state != RunSkipped || rec.Reason != "not approved: dismissed" || rec.Steps[len(rec.Steps)-1].Result != "skipped" {
		t.Fatalf("%s %+v", state, rec)
	}
	if strings.Contains(g.ranList(), "attended") {
		t.Fatal("a dismissed approval ran")
	}

	// Too old.
	g.now = g.now.Add(2 * time.Hour)
	g.observe("usb.new_device", map[string]float64{"1-3": 1})
	g.now = g.now.Add(ApprovalTTL)
	if err := g.e.ExpireApprovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.st.approvs[1].State != store.ApprovalExpired {
		t.Fatalf("%+v", g.st.approvs[1])
	}
	if state, rec := runRecord(t, g, 1); state != RunSkipped || rec.Reason != "not approved: it waited too long" {
		t.Fatalf("%s %+v", state, rec)
	}
}

func TestApprovalStaleAfterEditOrDisable(t *testing.T) {
	for _, change := range []func(a *store.Automation){
		func(a *store.Automation) { a.Updated++ },
		func(a *store.Automation) { a.Enabled = false },
	} {
		g, _ := approvalRig(t)
		g.add("heal", parkSpec)
		g.observe("usb.new_device", map[string]float64{"1-2": 1})
		g.st.mu.Lock()
		change(&g.st.autos[0])
		g.st.mu.Unlock()
		if _, err := approve(t, g, "automation/approve", "1"); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("approved a stale approval: %v", err)
		}
		if strings.Contains(g.ranList(), "attended") || g.st.approvs[0].State != store.ApprovalExpired {
			t.Fatalf("%q %+v", g.ranList(), g.st.approvs[0])
		}
	}
}

func TestApprovalRefusedWhilePaused(t *testing.T) {
	g, _ := approvalRig(t)
	g.add("heal", parkSpec)
	g.observe("usb.new_device", map[string]float64{"1-2": 1})
	if err := g.e.SetPaused(context.Background(), true, "you"); err != nil {
		t.Fatal(err)
	}
	if _, err := approve(t, g, "automation/approve", "1"); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("%v", err)
	}
	if g.st.approvs[0].State != store.ApprovalPending {
		t.Fatal("pausing keeps the approval, refused until resumed")
	}
	if err := g.e.SetPaused(context.Background(), false, "you"); err != nil {
		t.Fatal(err)
	}
	if _, err := approve(t, g, "automation/approve", "1"); err != nil {
		t.Fatal(err)
	}
}

func TestApprovedStepThatChangedDoesNotRun(t *testing.T) {
	g, _ := approvalRig(t)
	items := []actions.Item{{ID: "a"}}
	ran := false
	g.reg.Register(actions.Action{Kind: "box", Name: "remove", Risk: actions.Destructive, Authority: actions.AuthorityNone,
		Validate: func(actions.Request) error { return nil },
		Plan: func(context.Context, actions.Request) (actions.Plan, error) {
			return actions.Plan{Items: append([]actions.Item(nil), items...)}, nil
		},
		Run: func(context.Context, actions.Request, actions.Plan) (actions.Result, error) {
			ran = true
			return actions.Result{Done: 1}, nil
		}})
	g.add("box", `{"trigger":{"kind":"usb_device"},"then":[{"step":"action","action":"box/remove","target":"x"}]}`)
	g.observe("usb.new_device", map[string]float64{"1": 1})
	a, _ := g.reg.Lookup("automation/approve")
	r := actions.Request{Target: "1", Params: map[string]string{}}
	p, err := a.Plan(context.Background(), r)
	if err != nil || p.Risk != actions.Destructive {
		t.Fatalf("a destructive step makes the approval need the token: %v %+v", err, p)
	}
	items = append(items, actions.Item{ID: "b"}) // something new appeared since the preview
	if _, err := a.Run(context.Background(), r, p); err != nil {
		t.Fatal(err)
	}
	g.e.Wait()
	state, rec := runRecord(t, g, 0)
	if ran || state != RunFailed || !strings.Contains(rec.Steps[len(rec.Steps)-1].Detail, "changed since you approved") {
		t.Fatalf("%v %s %+v", ran, state, rec.Steps)
	}
}

func TestBranchParksTheRestOfTheRun(t *testing.T) {
	g, _ := approvalRig(t)
	g.add("branch", `{"trigger":{"kind":"usb_device"},"then":[
		{"step":"check","what":"container_health","target":"web","expect":"unhealthy",
		 "then":[{"step":"action","action":"container/restart","target":"web"},{"step":"action","action":"power/profile","target":"in-branch"}]},
		{"step":"action","action":"power/profile","target":"after"}]}`)
	g.observe("usb.new_device", map[string]float64{"1": 1})
	var p Parked
	if err := json.Unmarshal(g.st.approvs[0].Steps, &p); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range p.Steps {
		got = append(got, s.Target)
	}
	if strings.Join(got, ",") != "web,in-branch,after" {
		t.Fatalf("%v", got)
	}
	if _, err := approve(t, g, "automation/approve", "1"); err != nil {
		t.Fatal(err)
	}
	if r := g.ranList(); r != "container/restart web (attended),power/profile in-branch (attended),power/profile after (attended)" {
		t.Fatalf("%q", r)
	}
}

func TestApprovalRefusesProtectedAtPreview(t *testing.T) {
	g := protectedRig(t)
	g.e.RegisterApprovals(g.reg)
	// Parked before the target became protected.
	g.add("p", `{"trigger":{"kind":"usb_device"},"then":[{"step":"action","action":"image/remove","target":"x"}]}`)
	g.observe("usb.new_device", map[string]float64{"1": 1})
	g.e.cfg.Protect = func(_, target string) string { return "now protected" }
	if _, err := approve(t, g, "automation/approve", "1"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("%v", err)
	}
}

func TestApprovalActionsNeverInAnAutomation(t *testing.T) {
	for _, act := range []string{"automation/approve", "automation/dismiss", "mode/approve", "stack/grant"} {
		_, err := spec.Parse([]byte(`{"trigger":{"kind":"usb_device"},"then":[{"step":"action","action":"` + act + `","target":"1"}]}`))
		if err == nil {
			t.Errorf("%s accepted in an automation", act)
		}
	}
}

func TestBadApprovalTargets(t *testing.T) {
	g, _ := approvalRig(t)
	for _, id := range []string{"0", "-1", "01", "x", "99"} {
		if _, err := approve(t, g, "automation/approve", id); err == nil {
			t.Errorf("%q accepted", id)
		} else if id == "99" && !strings.Contains(err.Error(), "no such approval") {
			t.Errorf("%v", err)
		}
	}
	var ae *actions.Error
	_, err := approve(t, g, "automation/approve", "99")
	if !errors.As(err, &ae) || ae.Status != 404 {
		t.Fatalf("%v", err)
	}
}
