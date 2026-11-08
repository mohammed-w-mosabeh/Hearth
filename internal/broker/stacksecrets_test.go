package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/secrets"
)

const marker = "S3CR3T-marker-9f2"

func secretRig(t *testing.T) *lifeRig {
	t.Helper()
	r := newLifeRig(t)
	sp := strings.Replace(lifeSpec, `"environment"`, `"secrets":["PW","ADMIN_USER"],"environment"`, 1)
	if _, err := r.call(t, "stack.deploy", deployParams(sp)); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSecretsSetAndReveal(t *testing.T) {
	r := secretRig(t)
	if _, err := r.call(t, "stack.secrets.set", map[string]any{"name": "web", "set": map[string]string{"PW": marker + "$x\"\\", "ADMIN_USER": "admin"}}); err != nil {
		t.Fatal(err)
	}
	b, err := r.store.Read("web", FileSecrets)
	if err != nil {
		t.Fatal(err)
	}
	m, err := secrets.Parse(b)
	if err != nil || m["PW"] != marker+"$x\"\\" || m["ADMIN_USER"] != "admin" {
		t.Fatalf("%q %v", m, err)
	}
	got, err := r.call(t, "stack.secrets.reveal", map[string]string{"name": "web", "key": "PW"})
	if err != nil || got.(map[string]string)["value"] != marker+"$x\"\\" {
		t.Fatalf("%v %v", got, err)
	}

	// Status shows names, never values.
	st, err := r.call(t, "stack.status", map[string]string{"name": "web"})
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(st)
	if strings.Contains(string(js), marker) || strings.Contains(string(js), "admin\"") {
		t.Fatalf("status shows a value: %s", js)
	}
	info := st.([]StackInfo)[0]
	if len(info.Secrets) != 2 || info.Secrets[0].Key != "ADMIN_USER" || !info.Secrets[1].Set || info.Secrets[1].Services[0] != "app" {
		t.Fatalf("%+v", info.Secrets)
	}

	// Remove one; the other stays.
	if _, err := r.call(t, "stack.secrets.set", map[string]any{"name": "web", "remove": []string{"ADMIN_USER"}}); err != nil {
		t.Fatal(err)
	}
	b, _ = r.store.Read("web", FileSecrets)
	if m, _ := secrets.Parse(b); len(m) != 1 || m["PW"] == "" {
		t.Fatalf("%q", m)
	}
	if _, err := r.call(t, "stack.secrets.reveal", map[string]string{"name": "web", "key": "ADMIN_USER"}); errCode(err) != CodeBadParams {
		t.Fatalf("revealed a removed key: %v", err)
	}

	// A redeploy keeps secrets.env.
	sp := strings.Replace(lifeSpec, `"environment"`, `"secrets":["PW","ADMIN_USER"],"environment"`, 1)
	if _, err := r.call(t, "stack.deploy", deployParams(sp)); err != nil {
		t.Fatal(err)
	}
	if b2, _ := r.store.Read("web", FileSecrets); string(b2) != string(b) {
		t.Fatal("a deploy rewrote secrets.env")
	}
	for _, name := range []string{"stack.secrets.set", "stack.secrets.reveal"} {
		if m := r.methods[name]; !m.Privileged || m.Action != ActionStackSecrets || m.Check == nil {
			t.Fatalf("%s is not behind its action", name)
		}
	}
}

func TestSecretsRefusedBeforePolkit(t *testing.T) {
	r := secretRig(t)
	for name, p := range map[string]any{
		"unreferenced":  map[string]any{"name": "web", "set": map[string]string{"OTHER": marker}},
		"compose key":   map[string]any{"name": "web", "set": map[string]string{"COMPOSE_FILE": marker}},
		"newline":       map[string]any{"name": "web", "set": map[string]string{"PW": marker + "\nEVIL=1"}},
		"control":       map[string]any{"name": "web", "set": map[string]string{"PW": marker + "\x1b"}},
		"huge":          map[string]any{"name": "web", "set": map[string]string{"PW": strings.Repeat("x", secrets.MaxValue+1)}},
		"both":          map[string]any{"name": "web", "set": map[string]string{"PW": marker}, "remove": []string{"PW"}},
		"nothing":       map[string]any{"name": "web"},
		"missing stack": map[string]any{"name": "nope", "set": map[string]string{"PW": marker}},
		"unknown param": map[string]any{"name": "web", "set": map[string]string{"PW": marker}, "x": 1},
		"bad remove":    map[string]any{"name": "web", "remove": []string{"a-b"}},
	} {
		b, _ := json.Marshal(p)
		err := r.methods["stack.secrets.set"].Check(context.Background(), b)
		if errCode(err) != CodeBadParams {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("%s: the error quotes the value: %v", name, err)
		}
	}
	for name, p := range map[string]any{
		"unset":   map[string]string{"name": "web", "key": "PW"},
		"bad key": map[string]string{"name": "web", "key": "A-B"},
		"missing": map[string]string{"name": "nope", "key": "PW"},
	} {
		b, _ := json.Marshal(p)
		if err := r.methods["stack.secrets.reveal"].Check(context.Background(), b); errCode(err) != CodeBadParams {
			t.Errorf("reveal %s: %v", name, err)
		}
	}
	if _, err := r.store.Read("web", FileSecrets); err != nil {
		t.Fatal(err)
	}
	if b, _ := r.store.Read("web", FileSecrets); len(b) != 0 {
		t.Fatalf("a refused set wrote %q", b)
	}
}

// TestSecretsNeverHashedIntoAudit runs the real server: the audit of a set
// hashes only the stack and key names, and a malformed line nothing.
func TestSecretsNeverHashedIntoAudit(t *testing.T) {
	r := secretRig(t)
	var methods []Method
	for _, m := range r.methods {
		methods = append(methods, m)
	}
	h := start(t, true, func(c *Config) { c.Methods = append(c.Methods, methods...) })
	h.auth.allow = true
	params := `{"name":"web","set":{"PW":"` + marker + `"}}`
	resp := h.raw(`{"id":1,"method":"stack.secrets.set","params":`+params+`}`,
		`{"id":2,"method":"stack.secrets.set","params":{"name":"web","set":{"NOPE":"`+marker+`"}}}`,
		`{"id":3,"method":"stack.secrets.set","params":`+params+`} trailing`)
	if len(resp) != 3 || resp[0].Error != nil || resp[1].Error == nil {
		t.Fatalf("%+v", resp)
	}
	for _, rr := range resp {
		b, _ := json.Marshal(rr)
		if strings.Contains(string(b), marker) {
			t.Fatalf("a reply quotes the value: %s", b)
		}
	}
	forbidden := map[string]bool{}
	for _, p := range []string{params, `{"name":"web","set":{"NOPE":"` + marker + `"}}`} {
		sum := sha256.Sum256([]byte(p))
		forbidden[hex.EncodeToString(sum[:])] = true
	}
	h.audit.mu.Lock()
	defer h.audit.mu.Unlock()
	n := 0
	for _, rec := range h.audit.recs {
		if strings.Contains(rec.Result, marker) || forbidden[rec.ParamsHash] {
			t.Errorf("audit holds the value or its hash: %+v", rec)
		}
		if rec.Method == "stack.secrets.set" {
			n++
			want := sha256.Sum256(redactSecretsSet(json.RawMessage(params)))
			if rec.Result == "ok" && rec.ParamsHash != hex.EncodeToString(want[:]) {
				t.Errorf("the hash is not of the redacted params: %+v", rec)
			}
		}
		if rec.Method == "(malformed)" && rec.ParamsHash != "" {
			t.Errorf("a malformed line was hashed: %+v", rec)
		}
	}
	if n != 2 {
		t.Fatalf("%d set records", n)
	}
	if string(redactSecretsSet(json.RawMessage(params))) != `{"name":"web","remove":[],"set":["PW"]}` {
		t.Fatalf("%s", redactSecretsSet(json.RawMessage(params)))
	}
}

func TestInspectMasksStackSecrets(t *testing.T) {
	raw := json.RawMessage(`{"Id":"x","Config":{"Env":["ADMIN_USER=` + marker + `","TZ=UTC","DB_PASSWORD=p"],
		"Labels":{"org.hearth.secrets":"ADMIN_USER,PW","org.hearth.stack":"web"}}}`)
	out, err := maskEnvIn(raw, "Config")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, marker) || !strings.Contains(s, "TZ=UTC") || strings.Contains(s, "DB_PASSWORD=p\"") {
		t.Fatalf("%s", s)
	}
	// Without the label, a plain name stays visible.
	out, _ = maskEnvIn(json.RawMessage(`{"Config":{"Env":["ADMIN_USER=bob"]}}`), "Config")
	if !strings.Contains(string(out), "ADMIN_USER=bob") {
		t.Fatalf("%s", out)
	}
}
