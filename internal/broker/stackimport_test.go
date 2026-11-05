package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/secrets"
)

const importSpec = `{"version":1,"name":"web","bind_roots":["/srv/site"],"services":{"app":{"image":"nginx:1.27",
"secrets":["PW","POSTGRES_PASSWORD=APP_POSTGRES_PASSWORD"],
"volumes":[{"type":"bind","source":"/srv/site","target":"/usr/share/nginx/html"}]}}}`

func importParams(spec string, values map[string]string, flags ...string) map[string]any {
	if flags == nil {
		flags = []string{}
	}
	return map[string]any{"spec": json.RawMessage(spec), "ack_flags": flags, "secrets": values}
}

func foreignRig(t *testing.T) *lifeRig {
	t.Helper()
	r := newLifeRig(t)
	r.engine.containers = []StackContainer{{ID: "old", Name: "web-app-1", Service: "app", State: "running", PID: 42}} // no stack label
	return r
}

func TestStackImport(t *testing.T) {
	r := foreignRig(t)
	vals := map[string]string{"PW": marker, "APP_POSTGRES_PASSWORD": "p$" + marker}
	// deploy refuses a project Hearth does not manage.
	if _, err := r.call(t, "stack.deploy", deployParams(importSpec)); errCode(err) != CodeBadParams || !strings.Contains(err.Error(), "import it") {
		t.Fatalf("deploy over a foreign project: %v", err)
	}
	if _, err := r.call(t, "stack.import", importParams(importSpec, vals)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.ran, []string{"up"}) {
		t.Fatalf("ran %v", r.ran)
	}
	b, err := r.store.Read("web", FileSecrets)
	if err != nil {
		t.Fatal(err)
	}
	m, err := secrets.Parse(b)
	if err != nil || m["PW"] != marker || m["APP_POSTGRES_PASSWORD"] != "p$"+marker {
		t.Fatalf("%q %v", m, err)
	}
	if _, err := r.store.Read("web", FileCompose); err != nil {
		t.Fatal(err)
	}
	// Imported: the name is Hearth's, so deploy works and import does not.
	if _, err := r.call(t, "stack.import", importParams(importSpec, vals)); errCode(err) != CodeBadParams || !strings.Contains(err.Error(), "already an app") {
		t.Fatalf("second import: %v", err)
	}
	if _, err := r.call(t, "stack.deploy", deployParams(importSpec)); err != nil {
		t.Fatalf("deploy after import: %v", err)
	}
	if b2, _ := r.store.Read("web", FileSecrets); string(b2) != string(b) {
		t.Fatal("deploy rewrote the imported secrets")
	}
}

func TestStackImportRefusedBeforePolkit(t *testing.T) {
	vals := map[string]string{"PW": marker, "APP_POSTGRES_PASSWORD": marker}
	lan := strings.Replace(importSpec, `"volumes"`, `"ports":[{"host_ip":"0.0.0.0","host":8080,"container":80}],"volumes"`, 1)
	lan = strings.Replace(lan, `"name":"web",`, `"name":"web","opt_in":["port:0.0.0.0:8080/tcp"],`, 1)
	for name, c := range map[string]struct {
		params any
		setup  func(r *lifeRig)
		want   string
	}{
		"missing value": {importParams(importSpec, map[string]string{"PW": marker}), nil, "APP_POSTGRES_PASSWORD has no value"},
		"extra value":   {importParams(importSpec, map[string]string{"PW": marker, "APP_POSTGRES_PASSWORD": marker, "OTHER": marker}), nil, "names the secret OTHER"},
		"bad value":     {importParams(importSpec, map[string]string{"PW": marker + "\nX=1", "APP_POSTGRES_PASSWORD": marker}), nil, "one line"},
		"flag unacked":  {importParams(lan, vals), nil, "port:0.0.0.0:8080/tcp"},
		"no containers": {importParams(importSpec, vals), func(r *lifeRig) { r.engine.containers = nil }, "deploy it instead"},
		"managed":       {importParams(importSpec, vals), func(r *lifeRig) { r.engine.containers[0].Stack = "web" }, "already managed"},
		"path refused": {importParams(importSpec, vals), func(r *lifeRig) {
			r.paths.first.OK, r.paths.first.Refused = false, []string{"app: bind source /srv/site: it resolves to /etc"}
		}, "/etc"},
		"unknown param": {map[string]any{"spec": json.RawMessage(importSpec), "ack_flags": []string{}, "secrets": vals, "x": 1}, nil, "params are"},
		"yaml":          {importParams(`"services: {}"`, vals), nil, "not an object"},
	} {
		r := foreignRig(t)
		if c.setup != nil {
			c.setup(r)
		}
		b, _ := json.Marshal(c.params)
		err := r.methods["stack.import"].Check(context.Background(), b)
		if errCode(err) != CodeBadParams || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("%s: the error quotes a value: %v", name, err)
		}
		if len(r.ran) != 0 {
			t.Errorf("%s: ran %v", name, r.ran)
		}
	}
}

func TestStackImportPolkitAndAudit(t *testing.T) {
	r := foreignRig(t)
	audit := &memAudit{}
	methods := StackLifeMethods(StackConfig{Store: r.store, Paths: r.paths, Engine: r.engine, ProcRoot: r.proc, Audit: audit,
		Run: func(context.Context, string) error {
			r.engine.containers = []StackContainer{{ID: "c1", Name: "web-app-1", Service: "app", State: "running", Stack: "web", PID: 42}}
			return nil
		}})
	auth := &detailAuthority{}
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, methods...); c.Authority = auth; c.Audit = audit })
	lan := strings.Replace(importSpec, `"volumes"`, `"ports":[{"host_ip":"0.0.0.0","host":8080,"container":80}],"volumes"`, 1)
	lan = strings.Replace(lan, `"name":"web",`, `"name":"web","opt_in":["port:0.0.0.0:8080/tcp"],`, 1)
	params, _ := json.Marshal(importParams(lan, map[string]string{"PW": marker, "APP_POSTGRES_PASSWORD": marker}, "port:0.0.0.0:8080/tcp"))
	resp := h.raw(`{"id":1,"method":"stack.import","params":` + string(params) + `}`)
	if len(resp) != 1 || resp[0].Error != nil {
		t.Fatalf("%+v", resp)
	}
	if !slices.Equal(auth.actions, []string{ActionStackDanger}) || !strings.Contains(auth.details[0]["polkit.message"], "import the app web") {
		t.Fatalf("asked %v %v", auth.actions, auth.details)
	}
	sum := sha256.Sum256(params)
	audit.mu.Lock()
	defer audit.mu.Unlock()
	var sawImport, sawDanger bool
	for _, rec := range audit.recs {
		if strings.Contains(rec.Result, marker) || rec.ParamsHash == hex.EncodeToString(sum[:]) {
			t.Fatalf("audit holds a value or the params' hash: %+v", rec)
		}
		sawImport = sawImport || (rec.Method == "stack.import" && rec.Result == "ok")
		sawDanger = sawDanger || (rec.Method == "stack.import.danger" && strings.Contains(rec.Result, "port:0.0.0.0:8080/tcp"))
	}
	if !sawImport || !sawDanger {
		t.Fatalf("%+v", audit.recs)
	}
	if string(redactImport(params)) != `{"ack_flags":["port:0.0.0.0:8080/tcp"],"name":"web","secrets":["APP_POSTGRES_PASSWORD","PW"]}` {
		t.Fatalf("%s", redactImport(params))
	}
}
