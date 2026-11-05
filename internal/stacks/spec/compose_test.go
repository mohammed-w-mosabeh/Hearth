package spec

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func compose(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	s, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compose(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCompose(t *testing.T) {
	m := compose(t, []byte(good))
	if m["name"] != "photos" {
		t.Fatalf("name %v", m["name"])
	}
	srv := m["services"].(map[string]any)["server"].(map[string]any)
	if got := srv["command"].([]any); got[1] != "$$HOME" || got[2] != "$${NOT_INTERPOLATED}" {
		t.Fatalf("command %v", got)
	}
	env := srv["environment"].(map[string]any)
	if env["DB_PASSWORD"] != "${DB_PASSWORD}" || env["TZ"] != "Europe/Berlin" {
		t.Fatalf("environment %v", env)
	}
	if srv["labels"].(map[string]any)[LabelStack] != "photos" {
		t.Fatal("no stack label")
	}
	ports := srv["ports"].([]any)
	if p := ports[0].(map[string]any); p["host_ip"] != "127.0.0.1" || p["published"] != "2283" || p["protocol"] != "tcp" {
		t.Fatalf("port %v", p)
	}
	bind := srv["volumes"].([]any)[0].(map[string]any)
	if bind["bind"].(map[string]any)["create_host_path"] != false || bind["read_only"] != true {
		t.Fatalf("bind %v", bind)
	}
	if srv["devices"].([]any)[0] != "/dev/dri:/dev/dri" || srv["mem_limit"].(float64) != 4096<<20 {
		t.Fatalf("devices %v mem %v", srv["devices"], srv["mem_limit"])
	}
	db := m["services"].(map[string]any)["db"].(map[string]any)
	if env := db["environment"].(map[string]any); len(env) != 1 || env["DB_PASSWORD"] != "${DB_PASSWORD}" {
		t.Fatalf("db gets only its secret: %v", env)
	}
	vol := m["volumes"].(map[string]any)["pgdata"].(map[string]any)
	if vol["name"] != "photos_pgdata" || vol["external"] != true {
		t.Fatalf("volume %v", vol)
	}
}

func TestComposeIsStable(t *testing.T) {
	s, _ := Decode([]byte(good))
	a, _ := Compose(s)
	for range 20 {
		if b, _ := Compose(s); !bytes.Equal(a, b) {
			t.Fatal("two renderings differ")
		}
	}
}

func TestComposeRefusesARefusedSpec(t *testing.T) {
	s, _ := Decode([]byte(good))
	s.OptIn = nil
	if _, err := Compose(s); err == nil {
		t.Fatal("generated a refused spec")
	}
}

var dollar = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$`)

// walk visits every string of a decoded JSON value, with the service it
// is in.
func walk(v any, svc string, depth int, f func(svc, s string)) {
	switch x := v.(type) {
	case string:
		f(svc, x)
	case []any:
		for _, e := range x {
			walk(e, svc, depth+1, f)
		}
	case map[string]any:
		for k, e := range x {
			f(svc, k)
			next := svc
			if depth == 0 {
				next = k // services.<name>
			}
			walk(e, next, depth+1, f)
		}
	}
}

// FuzzCompose: from any accepted spec, every "$" in the file is either "$$"
// or a reference to a secret that service names.
func FuzzCompose(f *testing.F) {
	f.Add([]byte(good))
	f.Add([]byte(`{"version":1,"name":"a","services":{"a":{"image":"nginx","environment":{"A":"${B}$C$$D$"},"secrets":["S"]}}}`))
	f.Add([]byte(`{"version":1,"name":"a","services":{"a":{"image":"nginx","command":["$${S}","${S}"],"labels":{"l":"${S}"}}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		s, r, err := Parse(raw)
		if err != nil || !r.OK() {
			return
		}
		b, err := Compose(s)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		walk(m["services"], "", 0, func(svc, str string) {
			for _, g := range dollar.FindAllStringSubmatch(str, -1) {
				switch {
				case g[0] == "$$":
				case g[1] != "" && svc != "" && slices.Contains(s.Services[svc].Secrets, g[1]) && str == g[0]:
				default:
					t.Fatalf("%s: %q interpolates", svc, str)
				}
			}
		})
		if strings.Contains(string(b), `"privileged"`) {
			t.Fatal("privileged in the file")
		}
	})
}

func TestComposeRestartDefault(t *testing.T) {
	for in, want := range map[string]string{"": DefaultRestart, "no": "no", "always": "always"} {
		s := Spec{Version: 1, Name: "r", Services: map[string]Service{"w": {Image: "nginx:1.27", Restart: in}}}
		b, err := Compose(s)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Services map[string]struct{ Restart string }
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if got := out.Services["w"].Restart; got != want {
			t.Errorf("restart %q: got %q", in, got)
		}
	}
}

func TestComposeSecretsLabel(t *testing.T) {
	s := Spec{Version: 1, Name: "r", Services: map[string]Service{
		"w":     {Image: "nginx:1.27", Secrets: []string{"PW", "ADMIN_USER"}},
		"plain": {Image: "nginx:1.27"},
	}}
	b, err := Compose(s)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Services map[string]struct {
			Labels      map[string]string
			Environment map[string]string
		}
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.Services["w"].Labels[LabelSecrets]; got != "ADMIN_USER,PW" {
		t.Fatalf("label %q", got)
	}
	if _, ok := out.Services["plain"].Labels[LabelSecrets]; ok {
		t.Fatal("a service without secrets has the label")
	}
	if out.Services["w"].Environment["PW"] != "${PW}" {
		t.Fatalf("env %v", out.Services["w"].Environment)
	}
}

func TestServiceFlags(t *testing.T) {
	_, rep, err := Parse([]byte(`{"version":1,"name":"f","opt_in":["cap:NET_ADMIN","port:0.0.0.0:8443/tcp","docker-socket"],
"services":{"vpn":{"image":"x","cap_add":["NET_ADMIN"],"ports":[{"host_ip":"0.0.0.0","host":8443,"container":443}]},
"ui":{"image":"y","volumes":[{"type":"bind","source":"/run/docker.sock","target":"/var/run/docker.sock"}]},"plain":{"image":"z"}}}`))
	if err != nil || !rep.OK() {
		t.Fatalf("%v %v", err, rep.Refused)
	}
	want := map[string][]string{"vpn": {"cap:NET_ADMIN", "port:0.0.0.0:8443/tcp"}, "ui": {"docker-socket"}}
	if len(rep.ServiceFlags) != 2 || !slices.Equal(rep.ServiceFlags["vpn"], want["vpn"]) || !slices.Equal(rep.ServiceFlags["ui"], want["ui"]) {
		t.Fatalf("%v", rep.ServiceFlags)
	}
	if len(rep.Flags) != 3 {
		t.Fatalf("%v", rep.Flags)
	}
}

func TestDottedPlainEnv(t *testing.T) {
	s := Spec{Version: 1, Name: "es", Services: map[string]Service{"es": {Image: "elasticsearch:8", Environment: map[string]string{"discovery.type": "single-node", "xpack-x": "$v"}}}}
	b, err := Compose(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"discovery.type":"single-node"`) || !strings.Contains(string(b), `"xpack-x":"$$v"`) {
		t.Fatalf("%s", b)
	}
}
