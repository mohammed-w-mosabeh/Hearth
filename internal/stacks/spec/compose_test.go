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
