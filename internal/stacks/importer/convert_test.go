package importer

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/secret"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

func shape(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/shape.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func has(l []string, sub string) bool {
	for _, s := range l {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestConvertShape(t *testing.T) {
	r := Convert(Input{Project: "shape", Rendered: shape(t), TZ: "Asia/Riyadh"})
	enc, _ := json.Marshal(r)
	if strings.Contains(string(enc), "s3cretvalue") || strings.Contains(string(enc), "fromfile") {
		t.Fatalf("a secret value is shown: %s", enc)
	}
	if r.Values["DB_PASSWORD"] != "s3cretvalue" || r.Values["EXTRA"] != "fromfile" || r.Values["TZ"] != "UTC" {
		t.Fatalf("values %v", r.Values)
	}
	app := r.Spec.Services["app"]
	if !slices.Equal(app.Secrets, []string{"DB_PASSWORD", "EXTRA", "TZ"}) || len(app.Environment) != 0 {
		t.Fatalf("env %v %v", app.Secrets, app.Environment)
	}
	// The password interpolated into the command is masked and refused.
	if app.Command[1] != "********" || !has(r.Refused, "the value of DB_PASSWORD also appears in the project outside its environment") {
		t.Fatalf("command %v refused %v", app.Command, r.Refused)
	}
	// Ports: 8080 is on every address today, so it is a flag, kept and opted in.
	if len(app.Ports) != 2 || app.Ports[0].HostIP != "0.0.0.0" || app.Ports[1].HostIP != "127.0.0.1" || app.Ports[1].Protocol != "udp" {
		t.Fatalf("ports %+v", app.Ports)
	}
	if !slices.Contains(r.Flags, "port:0.0.0.0:8080/tcp") || !slices.Contains(r.Spec.OptIn, "port:0.0.0.0:8080/tcp") {
		t.Fatalf("flags %v", r.Flags)
	}
	// /etc/localtime is never bound: it becomes TZ, unless TZ is set.
	for _, m := range app.Volumes {
		if m.Source == "/etc/localtime" {
			t.Fatal("/etc/localtime kept")
		}
	}
	if !has(r.Changes, "time zone bind becomes TZ=Asia/Riyadh") {
		t.Fatalf("%v", r.Changes)
	}
	if !slices.Equal(r.Spec.BindRoots, []string{"/srv/shape/data"}) || app.Volumes[1].Source != "model" {
		t.Fatalf("binds %v %+v", r.Spec.BindRoots, app.Volumes)
	}
	if app.ShmSizeMB != 128 || app.MemoryMB != 512 || app.StopGraceS != 60 {
		t.Fatalf("sizes %+v", app)
	}
	if h := app.Healthcheck; h == nil || h.IntervalS != 90 || h.TimeoutS != 5 || h.StartPeriodS != 10 || h.Retries != 3 {
		t.Fatalf("health %+v", app.Healthcheck)
	}
	if !slices.Equal(app.DependsOn, []string{"cache"}) || !has(r.Changes, "no longer until healthy") {
		t.Fatalf("deps %v %v", app.DependsOn, r.Changes)
	}
	if !slices.Equal(app.Networks, []string{"front"}) || r.Spec.Services["cache"].Networks != nil {
		t.Fatalf("networks %v", app.Networks)
	}
	if _, ok := r.Spec.Networks["front"]; !ok || r.Spec.Networks["front"].Name != "" {
		t.Fatalf("top networks %+v", r.Spec.Networks)
	}
	if _, ok := r.Spec.Networks["default"]; ok {
		t.Fatal("the default network is Compose's own")
	}
	if v := r.Spec.Volumes["model"]; v.Name != "" {
		t.Fatalf("volume name %+v: shape_model is the default", v)
	}
	if !has(r.Refused, "app: stdin_open is not supported") || !has(r.Refused, "app: tty is not supported") || !has(r.Changes, "expose is dropped") {
		t.Fatalf("refused %v changes %v", r.Refused, r.Changes)
	}
	if _, ok := r.Spec.Services["db"]; ok {
		t.Fatal("a profiled service was rendered")
	}
}

func TestConvertPlainAndSkip(t *testing.T) {
	// Without stdin_open, tty and the interpolated password, and with the
	// owner's choices, the project converts with no refusal.
	var doc map[string]any
	_ = json.Unmarshal(shape(t), &doc)
	app := doc["services"].(map[string]any)["app"].(map[string]any)
	delete(app, "stdin_open")
	delete(app, "tty")
	app["command"] = []any{"--pass-file", "/run/pw"}
	b, _ := json.Marshal(doc)
	r := Convert(Input{Project: "shape", Rendered: b, Plain: map[string]bool{"TZ": true}, TZ: "UTC"})
	if len(r.Refused) != 0 {
		t.Fatalf("refused %v", r.Refused)
	}
	got := r.Spec.Services["app"]
	if got.Environment["TZ"] != "UTC" || slices.Contains(got.Secrets, "TZ") {
		t.Fatalf("plain TZ %+v", got)
	}
	// The spec passes the real check and generates Compose.
	raw, _ := json.Marshal(r.Spec)
	if _, rep, err := spec.Parse(raw); err != nil || !rep.OK() {
		t.Fatalf("%v %v", err, rep.Refused)
	}
	if _, err := spec.Compose(r.Spec); err != nil {
		t.Fatal(err)
	}

	r = Convert(Input{Project: "shape", Rendered: b, Skip: map[string]bool{"cache": true}})
	if !has(r.Refused, "depends on cache, which is left out") {
		t.Fatalf("%v", r.Refused)
	}
	r = Convert(Input{Project: "shape", Rendered: b, Skip: map[string]bool{"nope": true}})
	if !has(r.Refused, "nope: is not a service") {
		t.Fatalf("%v", r.Refused)
	}
}

func TestConvertRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		rendered string
		want     string
	}{
		"bad name":     {`{"services":{"a":{"image":"x"}}}`, "cannot be a stack name"},
		"not json":     {`services: {}`, "not a JSON object"},
		"secrets":      {`{"services":{"a":{"image":"x"}},"secrets":{"s":{}}}`, `top-level "secrets"`},
		"privileged":   {`{"services":{"a":{"image":"x","privileged":true}}}`, "privileged is not supported"},
		"build only":   {`{"services":{"a":{"build":{"context":"."}}}}`, "built from source"},
		"port range":   {`{"services":{"a":{"image":"x","ports":[{"target":80,"published":"8000-8010"}]}}}`, "port range"},
		"two values":   {`{"services":{"a":{"image":"x","environment":{"PW":"one111"}},"b":{"image":"y","environment":{"PW":"two222"}}}}`, "PW has different values in a and b"},
		"dotted name":  {`{"services":{"a":{"image":"x","environment":{"discovery.type":"single-node"}}}}`, "cannot name a secret; mark it plain"},
		"url in label": {`{"services":{"a":{"image":"x","labels":{"l":"postgres://u:p@db/x"}}}}`, "a label holds a URL with a user"},
		"nfs volume":   {`{"services":{"a":{"image":"x"}},"volumes":{"v":{"driver":"nfs"}}}`, "driver nfs"},
		"etc bind":     {`{"services":{"a":{"image":"x","volumes":[{"type":"bind","source":"/etc/ssl","target":"/ssl"}]}}}`, "never allowed"},
		"newline":      {`{"services":{"a":{"image":"x","environment":{"K":"a\nb"}}}}`, "K: a value is one line"},
	} {
		project := "p"
		if name == "bad name" {
			project = "../x"
		}
		r := Convert(Input{Project: project, Rendered: []byte(c.rendered)})
		if !has(r.Refused, c.want) {
			t.Errorf("%s: %v", name, r.Refused)
		}
		enc, _ := json.Marshal(r)
		for _, v := range []string{"one111", "two222", "u:p@"} {
			if strings.Contains(string(enc), v) {
				t.Errorf("%s: shows %q: %s", name, v, enc)
			}
		}
	}
	// Two services with the same value share one secret.
	r := Convert(Input{Project: "p", Rendered: []byte(`{"services":{"a":{"image":"x","environment":{"PW":"same12"}},"b":{"image":"y","environment":{"PW":"same12"}}}}`)})
	if len(r.Refused) != 0 || len(r.Secrets) != 1 || !slices.Equal(r.Secrets[0].Services, []string{"a", "b"}) {
		t.Fatalf("%+v", r)
	}
	// A dotted name marked plain is kept.
	r = Convert(Input{Project: "p", Rendered: []byte(`{"services":{"a":{"image":"x","environment":{"discovery.type":"single-node"}}}}`), Plain: map[string]bool{"discovery.type": true}})
	if len(r.Refused) != 0 || r.Spec.Services["a"].Environment["discovery.type"] != "single-node" {
		t.Fatalf("%+v", r)
	}
}

func FuzzConvert(f *testing.F) {
	b, _ := os.ReadFile("testdata/shape.json")
	f.Add(b)
	f.Add([]byte(`{"services":{"a":{"image":"x","environment":{"DB_PASSWORD":"hunter22"},"command":["hunter22"],"hostname":"hunter22","labels":{"l":"x hunter22"}}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		r := Convert(Input{Project: "p", Rendered: raw, TZ: "UTC"})
		if _, err := json.Marshal(r); err != nil {
			t.Fatal(err)
		}
		// A value S19 counts as secret never reaches the spec's free text.
		for k, v := range r.Values {
			if len(v) < minFree || (!secret.Name(k) && !secret.Value(v)) {
				continue
			}
			for _, s := range freeText(r) {
				if strings.Contains(s, v) {
					t.Fatalf("the value of %s is shown in %q", k, s)
				}
			}
		}
		if len(r.Refused) == 0 && len(r.Spec.Services) > 0 {
			if _, err := spec.Compose(r.Spec); err != nil {
				t.Fatalf("accepted but does not compose: %v", err)
			}
		}
	})
}

// freeText is every string of the result that is not a structural name.
func freeText(r Result) []string {
	out := append(append([]string{}, r.Refused...), r.Changes...)
	for _, s := range r.Spec.Services {
		out = append(append(out, s.Command...), s.Entrypoint...)
		if s.Healthcheck != nil {
			out = append(out, s.Healthcheck.Test...)
		}
		for _, v := range s.Labels {
			out = append(out, v)
		}
		for _, v := range s.ExtraHosts {
			out = append(out, v)
		}
		for _, v := range s.Sysctls {
			out = append(out, v)
		}
		out = append(out, s.User, s.Hostname, s.WorkingDir)
	}
	return out
}

func TestConvertRealShapes(t *testing.T) {
	// A healthcheck with no test keeps the image's own.
	r := Convert(Input{Project: "p", Rendered: []byte(`{"services":{"a":{"image":"x","healthcheck":{"interval":"30s"}},"b":{"image":"y","healthcheck":{"disable":false}}}}`)})
	if len(r.Refused) != 0 || r.Spec.Services["a"].Healthcheck != nil || r.Spec.Services["b"].Healthcheck != nil || !has(r.Changes, "image's own healthcheck") {
		t.Fatalf("%+v", r)
	}
	// A password that is also a service's name hides nothing: not masked.
	r = Convert(Input{Project: "p", Rendered: []byte(`{"services":{"workflow":{"image":"x","environment":{"POSTGRES_PWD":"workflow"}},
		"workflow-ui":{"image":"y","healthcheck":{"test":["CMD","curl","workflow:7233"]}}}}`)})
	if len(r.Refused) != 0 || r.Spec.Services["workflow-ui"].Healthcheck.Test[2] != "workflow:7233" {
		t.Fatalf("%+v", r)
	}
	// A dotted name is refused once, and not listed as a secret.
	r = Convert(Input{Project: "p", Rendered: []byte(`{"services":{"es":{"image":"x","environment":{"discovery.type":"single-node"}}}}`)})
	if len(r.Refused) != 1 || len(r.Spec.Services["es"].Secrets) != 0 {
		t.Fatalf("%v", r.Refused)
	}
}
