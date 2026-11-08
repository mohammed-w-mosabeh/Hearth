package importer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

type importBroker struct {
	fakeBroker
	imported *broker.StackImportParams
	err      error
}

func (b *importBroker) Call(ctx context.Context, method string, params, out any) error {
	if method == "stack.import" {
		v := params.(broker.StackImportParams)
		b.imported = &v
		return b.err
	}
	return b.fakeBroker.Call(ctx, method, params, out)
}

// cleanShape is shape.json without what refuses it.
func cleanShape(t *testing.T) []byte {
	t.Helper()
	var doc map[string]any
	_ = json.Unmarshal(shape(t), &doc)
	app := doc["services"].(map[string]any)["app"].(map[string]any)
	delete(app, "stdin_open")
	delete(app, "tty")
	app["command"] = []any{"--pass-file", "/run/pw"}
	b, _ := json.Marshal(doc)
	return b
}

func TestImportAction(t *testing.T) {
	noConflict := strings.Replace(shapeContainers, `"PublicPort":8080`, `"PublicPort":18080`, 1)
	ib := &importBroker{fakeBroker: fakeBroker{containers: noConflict, volumes: `{"Volumes":[{"Name":"shape_model"}]}`}}
	d := Deps{Broker: ib, TZ: func() string { return "UTC" },
		Run: func(context.Context, string, ...string) (safeexec.Result, error) {
			return safeexec.Result{Stdout: cleanShape(t)}, nil
		}}
	reg := actions.NewRegistry(nil)
	RegisterAction(reg, d)
	a, ok := reg.Lookup("stack/import")
	if !ok || a.Risk != actions.Destructive {
		t.Fatal("stack/import")
	}
	r := actions.Request{Target: "shape", Params: map[string]string{"plain": "TZ"}}
	if err := a.Validate(r); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Blocked) != 0 || p.Confirm != "shape" {
		t.Fatalf("%+v", p)
	}
	enc, _ := json.Marshal(p)
	if strings.Contains(string(enc), "s3cretvalue") || strings.Contains(string(enc), "fromfile") {
		t.Fatalf("the preview shows a value: %s", enc)
	}
	for _, want := range []string{"recreated once", "reused in place", "secrets.env, not shown", "danger:", "left as they are"} {
		if !strings.Contains(string(enc), want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	res, err := a.Run(context.Background(), r, p)
	if err != nil || res.Done == 0 || len(res.Failed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	got := ib.imported
	if got == nil || got.Secrets["DB_PASSWORD"] != "s3cretvalue" || got.Secrets["EXTRA"] != "fromfile" || strings.Join(got.AckFlags, ",") != "port:0.0.0.0:8080/tcp" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(string(got.Spec), `"name":"shape"`) || !strings.Contains(string(got.Spec), `"TZ":"UTC"`) {
		t.Fatalf("spec %s", got.Spec)
	}

	// A refused project is blocked, and nothing reaches the broker.
	ib.imported = nil
	d.Run = func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{Stdout: shape(t)}, nil
	}
	reg = actions.NewRegistry(nil)
	RegisterAction(reg, d)
	a, _ = reg.Lookup("stack/import")
	p, err = a.Plan(context.Background(), r)
	if err != nil || len(p.Blocked) == 0 || p.State != nil {
		t.Fatalf("%+v %v", p, err)
	}
	if res, _ := a.Run(context.Background(), r, p); res.Done != 0 || ib.imported != nil {
		t.Fatal("a blocked import ran")
	}
	if a.Validate(actions.Request{Target: "shape", Params: map[string]string{"secrets": "x"}}) == nil {
		t.Fatal("an unknown param validated")
	}
	if a.Validate(actions.Request{Target: "../x"}) == nil {
		t.Fatal("a bad target validated")
	}

	// polkit refused: a failed item, not an error.
	ib.err = broker.ErrUnauthorized
	d.Run = func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{Stdout: cleanShape(t)}, nil
	}
	reg = actions.NewRegistry(nil)
	RegisterAction(reg, d)
	a, _ = reg.Lookup("stack/import")
	p, _ = a.Plan(context.Background(), r)
	if res, err := a.Run(context.Background(), r, p); err != nil || len(res.Failed) != 1 || res.Failed[0].Reason != ReasonUnauthorized {
		t.Fatalf("%+v %v", res, err)
	}
}
