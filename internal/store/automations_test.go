package store

import (
	"context"
	"encoding/json"
	"errors"
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
