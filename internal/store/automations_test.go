package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestAutomations(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	spec := json.RawMessage(`{"trigger":{"kind":"login"},"then":[{"step":"wait","seconds":1}]}`)
	ids, err := db.CreateAutomations(ctx, []Automation{{Name: "a", Enabled: true, Spec: spec, Origin: "ui"}, {Name: "b", Spec: spec, Origin: "import"}})
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	if _, err := db.CreateAutomations(ctx, []Automation{{Name: "c", Spec: spec, Origin: "ui"}, {Name: "d", Spec: json.RawMessage(`{`), Origin: "ui"}}); err == nil {
		t.Fatal("an import with a bad row was half stored")
	}
	all, err := db.Automations(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "a" || !all[0].Enabled || all[1].Enabled {
		t.Fatalf("%+v %v", all, err)
	}
	a := all[1]
	a.Name, a.Enabled = "b2", true
	if err := db.UpdateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAutomationEnabled(ctx, ids[0], false); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAutomation(ctx, ids[1])
	if err != nil || got.Name != "b2" || !got.Enabled {
		t.Fatalf("%+v %v", got, err)
	}
	rid, err := db.SaveAutomationRun(ctx, AutomationRun{AutomationID: ids[1], Started: time.Now().UnixMilli(), State: "running", Record: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveAutomationRun(ctx, AutomationRun{ID: rid, Finished: time.Now().UnixMilli(), State: "done", Record: json.RawMessage(`{"steps":[]}`)}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	_, _ = db.SaveAutomationRun(ctx, AutomationRun{AutomationID: ids[1], Started: old, State: "done", Record: json.RawMessage(`{}`)})
	_, _ = db.SaveAutomationRun(ctx, AutomationRun{AutomationID: ids[1], Started: old, State: "waiting", Record: json.RawMessage(`{}`)})
	if err := db.PruneAutomationRuns(ctx, time.Now().Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	runs, err := db.AutomationRuns(ctx, ids[1], 10)
	if err != nil || len(runs) != 2 || runs[0].State != "done" || runs[1].State != "waiting" {
		t.Fatalf("%+v %v", runs, err)
	}
	if err := db.DeleteAutomation(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	if runs, _ := db.AutomationRuns(ctx, 0, 10); len(runs) != 0 {
		t.Fatal("runs outlived their automation")
	}
	for _, err := range []error{db.DeleteAutomation(ctx, 999), db.UpdateAutomation(ctx, Automation{ID: 999, Spec: spec}), db.SetAutomationEnabled(ctx, 999, true)} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%v", err)
		}
	}
	if _, err := db.GetAutomation(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Error(err)
	}
}

func TestAutomationApprovals(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ids, err := db.CreateAutomations(ctx, []Automation{{Name: "a", Enabled: true, Spec: json.RawMessage(`{}`), Origin: "ui"}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.SaveAutomationRun(ctx, AutomationRun{AutomationID: ids[0], Started: 1, State: "waiting", Record: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := db.GetAutomationRun(ctx, run); err != nil || r.State != "waiting" {
		t.Fatal(r, err)
	}
	if _, err := db.GetAutomationRun(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	id, err := db.AddAutomationApproval(ctx, AutomationApproval{RunID: run, Created: 10, Expires: 20, Steps: json.RawMessage(`{"steps":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddAutomationApproval(ctx, AutomationApproval{RunID: run, Steps: json.RawMessage(`{`)}); err == nil {
		t.Fatal("invalid JSON stored")
	}
	ap, err := db.AutomationApprovalByID(ctx, id)
	if err != nil || ap.AutomationID != ids[0] || ap.State != ApprovalPending || ap.Expires != 20 {
		t.Fatalf("%+v %v", ap, err)
	}
	list, err := db.PendingAutomationApprovals(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if err := db.DecideAutomationApproval(ctx, id, ApprovalPending); err == nil {
		t.Fatal("decided to pending")
	}
	if err := db.DecideAutomationApproval(ctx, id, ApprovalApproved); err != nil {
		t.Fatal(err)
	}
	if err := db.DecideAutomationApproval(ctx, id, ApprovalDismissed); !errors.Is(err, ErrNotFound) {
		t.Fatal("one decision wins", err)
	}
	if list, _ := db.PendingAutomationApprovals(ctx); len(list) != 0 {
		t.Fatal(list)
	}
	if err := db.PruneAutomationApprovals(ctx, time.UnixMilli(11)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AutomationApprovalByID(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("a decided old approval is pruned", err)
	}
	// Deleting the automation removes its approvals.
	run2, _ := db.SaveAutomationRun(ctx, AutomationRun{AutomationID: ids[0], Started: 2, State: "waiting", Record: json.RawMessage(`{}`)})
	id2, _ := db.AddAutomationApproval(ctx, AutomationApproval{RunID: run2, Created: 10, Expires: 20, Steps: json.RawMessage(`{}`)})
	if err := db.DeleteAutomation(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AutomationApprovalByID(ctx, id2); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestMigrateAlertRules(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	defaults, err := db.AlertRules(ctx)
	if err != nil || len(defaults) == 0 {
		t.Fatal("the SQL migrations add default rules", err)
	}
	// An automation already holds ID 1, so new IDs collide with old ones.
	if _, err := db.CreateAutomations(ctx, []Automation{{Name: "x", Spec: json.RawMessage(`{}`), Origin: "ui"}}); err != nil {
		t.Fatal(err)
	}
	first, second := defaults[0], defaults[1]
	for _, r := range []AlertRule{first, second} {
		if _, err := db.OpenAlertEvent(ctx, AlertEvent{RuleID: r.ID, RuleName: r.Name, Metric: r.Metric, Severity: "warning", Op: ">", StartedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	bad := defaults[2].ID
	n, err := db.MigrateAlertRules(ctx, func(r AlertRule) (json.RawMessage, error) {
		if r.ID == bad {
			return json.RawMessage(`{"name":"invalid"}`), errors.New("invalid")
		}
		return json.RawMessage(`{"rule":` + strconv.FormatInt(r.ID, 10) + `}`), nil
	})
	if err != nil || n != len(defaults) {
		t.Fatal(n, err)
	}
	if left, _ := db.AlertRules(ctx); len(left) != 0 {
		t.Fatalf("rows left: %+v", left)
	}
	autos, _ := db.Automations(ctx)
	if len(autos) != len(defaults)+1 {
		t.Fatalf("%d automations", len(autos))
	}
	newID := map[int64]int64{}
	for _, a := range autos[1:] {
		if a.Origin != "alert" {
			t.Fatalf("%+v", a)
		}
		var v struct{ Rule int64 }
		if json.Unmarshal(a.Spec, &v) == nil && v.Rule != 0 {
			newID[v.Rule] = a.ID
		} else if a.Enabled {
			t.Fatal("an invalid rule must be stored turned off")
		}
	}
	events, err := db.AlertEvents(ctx, 10, 0)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	for _, e := range events {
		want := newID[first.ID]
		if e.RuleName == second.Name {
			want = newID[second.ID]
		}
		if e.RuleID != want {
			t.Fatalf("incident of %s points at %d, want %d", e.RuleName, e.RuleID, want)
		}
	}
	// Idempotent: nothing left to move.
	if n, err := db.MigrateAlertRules(ctx, func(AlertRule) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	// A conversion that fails outright moves nothing.
	if _, err := db.CreateAlertRule(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateAlertRules(ctx, func(AlertRule) (json.RawMessage, error) { return nil, errors.New("boom") }); err == nil {
		t.Fatal("expected an error")
	}
	if left, _ := db.AlertRules(ctx); len(left) != 1 {
		t.Fatal("a failed migration must leave the rows", left)
	}
}

type fakeRules struct{ created int }

func (f *fakeRules) AlertRules(context.Context) ([]AlertRule, error) {
	return []AlertRule{{ID: 42, Name: "elsewhere"}}, nil
}
func (f *fakeRules) CreateAlertRule(context.Context, AlertRule) (int64, error) {
	f.created++
	return 7, nil
}
func (f *fakeRules) UpdateAlertRule(context.Context, AlertRule) error { return ErrNotFound }
func (f *fakeRules) DeleteAlertRule(context.Context, int64) error     { return ErrNotFound }

func TestAlertRuleStoreDelegates(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	f := &fakeRules{}
	db.SetAlertRuleStore(f)
	if rules, _ := db.AlertRules(ctx); len(rules) != 1 || rules[0].ID != 42 {
		t.Fatal(rules)
	}
	if id, _ := db.CreateAlertRule(ctx, AlertRule{}); id != 7 || f.created != 1 {
		t.Fatal(id)
	}
	if !errors.Is(db.UpdateAlertRule(ctx, AlertRule{ID: 1}), ErrNotFound) || !errors.Is(db.DeleteAlertRule(ctx, 1), ErrNotFound) {
		t.Fatal("update and delete go to the store")
	}
}
