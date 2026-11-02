package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func composeOf(extra string) []byte {
	return []byte(`{"name":"app","services":{"web":{"image":"nginx:1.27","command":null,"entrypoint":null,
		"environment":{"A":"1"},"networks":{"default":null},"ports":[{"mode":"ingress","target":80,"published":"8080","protocol":"tcp"}],
		"volumes":[{"type":"bind","source":"/home/me/data","target":"/data","bind":{}},{"type":"volume","source":"db","target":"/db"}]` +
		extra + `}},"networks":{"default":{"name":"app_default","ipam":{}}},"volumes":{"db":{"name":"app_db"}}}`)
}

func TestCheckComposeAcceptsAPlainProject(t *testing.T) {
	c, err := CheckCompose(composeOf(`,"x-note":"hi","restart":"unless-stopped","deploy":{"resources":{"limits":{"cpus":"1","memory":"100M"}}}`), "app")
	if err != nil || len(c.Refused) > 0 {
		t.Fatal(err, c.Refused)
	}
	if c.Services["web"] != "nginx:1.27" {
		t.Errorf("services: %v", c.Services)
	}
}

func TestCheckComposeRefusesRiskyServices(t *testing.T) {
	for extra, want := range map[string]string{
		`,"privileged":true`:                                "privileged",
		`,"privileged":false`:                               "privileged", // listed keys only: present is enough
		`,"cap_add":["SYS_ADMIN"]`:                          "cap_add",
		`,"pid":"host"`:                                     "pid",
		`,"ipc":"host"`:                                     "ipc",
		`,"uts":"host"`:                                     "uts",
		`,"userns_mode":"host"`:                             "userns_mode",
		`,"cgroup":"host"`:                                  "cgroup",
		`,"cgroup_parent":"x"`:                              "cgroup_parent",
		`,"network_mode":"host"`:                            "network_mode host",
		`,"network_mode":"container:abc"`:                   "network_mode container:abc",
		`,"network_mode":"service:nope"`:                    "network_mode service:nope",
		`,"devices":[{"source":"/dev/sda"}]`:                "devices",
		`,"device_cgroup_rules":["a *:* rwm"]`:              "device_cgroup_rules",
		`,"security_opt":["seccomp=unconfined"]`:            "security_opt",
		`,"sysctls":{"net.ipv4.ip_forward":"1"}`:            "sysctls",
		`,"runtime":"x"`:                                    "runtime",
		`,"build":{"context":"."}`:                          "build",
		`,"provider":{"type":"awesomecloud"}`:               "provider",
		`,"post_start":[{"command":"x"}]`:                   "post_start",
		`,"develop":{}`:                                     "develop",
		`,"volumes_from":["other"]`:                         "volumes_from",
		`,"secrets":[{"source":"s"}]`:                       "secrets",
		`,"configs":[{"source":"s"}]`:                       "configs",
		`,"group_add":["docker"]`:                           "group_add",
		`,"logging":{"driver":"syslog"}`:                    "logging driver syslog",
		`,"pull_policy":"build"`:                            "pull_policy",
		`,"deploy":{"placement":{}}`:                        "deploy.placement",
		`,"deploy":{"resources":{"limits":{"devices":[]}}}`: "deploy.resources.limits.devices",
		`,"some_future_key":true`:                           "some_future_key",
	} {
		c, err := CheckCompose(composeOf(extra), "app")
		if err != nil {
			t.Errorf("%s: %v", extra, err)
			continue
		}
		if !strings.Contains(strings.Join(c.Refused, "\n"), "web: "+want) {
			t.Errorf("%s: refused %v, want %q", extra, c.Refused, want)
		}
	}
}

func TestCheckComposeRefusesRiskyMounts(t *testing.T) {
	for _, src := range []string{"/", "/etc", "/etc/ssh", "/root", "/proc", "/sys", "/dev", "/boot", "/var/run/docker.sock", "/run/docker.sock", "relative", "/home/../etc"} {
		raw := []byte(`{"name":"app","services":{"web":{"image":"nginx","volumes":[{"type":"bind","source":"` + src + `","target":"/x","bind":{}}]}}}`)
		c, err := CheckCompose(raw, "app")
		if err != nil || len(c.Refused) == 0 {
			t.Errorf("%s: %v %v", src, err, c.Refused)
		}
	}
	for _, v := range []string{
		`{"type":"bind","source":"/home/me","target":"/x","bind":{"propagation":"rshared"}}`,
		`{"type":"npipe","source":"x","target":"/x"}`,
		`{"type":"cluster","source":"x","target":"/x"}`,
		`"/etc:/x"`,
	} {
		raw := []byte(`{"name":"app","services":{"web":{"image":"nginx","volumes":[` + v + `]}}}`)
		if c, err := CheckCompose(raw, "app"); err != nil || len(c.Refused) == 0 {
			t.Errorf("%s: %v %v", v, err, c.Refused)
		}
	}
}

func TestCheckComposeRefusesTopLevelTricks(t *testing.T) {
	base := `"name":"app","services":{"web":{"image":"nginx"}}`
	for _, raw := range []string{
		`{` + base + `,"secrets":{"s":{"file":"/etc/shadow"}}}`,
		`{` + base + `,"configs":{"s":{"file":"/etc/shadow"}}}`,
		`{` + base + `,"include":["x"]}`,
		// A local volume with these options is a bind mount of anything.
		`{` + base + `,"volumes":{"v":{"driver":"local","driver_opts":{"device":"/etc","o":"bind","type":"none"}}}}`,
		`{` + base + `,"volumes":{"v":{"driver":"evil"}}}`,
		`{` + base + `,"networks":{"n":{"driver":"macvlan"}}}`,
		`{` + base + `,"networks":{"n":{"driver_opts":{"parent":"eth0"}}}}`,
		`{"name":"other","services":{"web":{"image":"nginx"}}}`,
		`{"name":"app","services":{}}`,
		`{"name":"app","services":{"web":{}}}`,
		`{"name":"app","services":{"web":{"image":"-oops"}}}`,
		`{"name":"app","services":{"web":3}}`,
	} {
		if c, err := CheckCompose([]byte(raw), "app"); err != nil || len(c.Refused) == 0 {
			t.Errorf("%s: %v %v", raw, err, c.Refused)
		}
	}
	for _, raw := range []string{``, `null`, `[]`, `{`, `{} {}`, strings.Repeat(" ", MaxComposeConfig+1)} {
		if _, err := CheckCompose([]byte(raw), "app"); err == nil {
			t.Errorf("%.20q was accepted as a rendering", raw)
		}
	}
}

// The value written is the one checked: with duplicate keys, Go keeps the last
// and another parser might not, so nothing of the original text survives.
func TestDuplicateKeysCannotHideFromTheCheck(t *testing.T) {
	raw := []byte(`{"name":"app","services":{"web":{"image":"nginx","privileged":true,"privileged":false}}}`)
	if c, err := CheckCompose(raw, "app"); err != nil || len(c.Refused) == 0 {
		t.Fatalf("%v %v", err, c.Refused)
	}
}

func composeCall(t *testing.T, cfg ComposeConfig, p ComposeUpdateParams) (any, error) {
	t.Helper()
	ms := ComposeMethods(cfg)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms[0].Check(context.Background(), raw); err != nil {
		return nil, err
	}
	return ms[0].Handle(context.Background(), Caller{}, raw)
}

func TestComposeUpdateMethod(t *testing.T) {
	dir := t.TempDir()
	cfg := ComposeConfig{File: filepath.Join(dir, "c.json"), Args: filepath.Join(dir, "c.args"),
		Services: func(context.Context, string) ([]string, error) { return []string{"web"}, nil },
		Bus:      func(context.Context) (SystemBus, error) { return nil, errors.New("no bus here") }}
	good := ComposeUpdateParams{Project: "app", Dir: "/home/me/app", Files: []string{"/home/me/app/compose.yaml"}, Config: composeOf("")}
	if ms := ComposeMethods(cfg); !ms[0].Privileged || ms[0].Action != ActionCompose {
		t.Error("compose.update must go through polkit")
	}
	for name, p := range map[string]ComposeUpdateParams{
		"option as project": {Project: "-x", Dir: good.Dir, Files: good.Files, Config: good.Config},
		"relative file":     {Project: "app", Dir: good.Dir, Files: []string{"compose.yaml"}, Config: good.Config},
		"no files":          {Project: "app", Dir: good.Dir, Config: good.Config},
		"comma in a file":   {Project: "app", Dir: good.Dir, Files: []string{"/a,b"}, Config: good.Config},
		"newline in dir":    {Project: "app", Dir: "/a\nCOMPOSE_PROJECT=x", Files: good.Files, Config: good.Config},
		"risky config":      {Project: "app", Dir: good.Dir, Files: good.Files, Config: composeOf(`,"privileged":true`)},
	} {
		var be *Error
		if _, err := composeCall(t, cfg, p); !errors.As(err, &be) || be.Code != CodeBadParams {
			t.Errorf("%s: %v, want bad params", name, err)
		}
	}
	// Unknown fields never reach the check.
	if err := ComposeMethods(cfg)[0].Check(context.Background(), json.RawMessage(`{"project":"app","exec":"sh"}`)); err == nil {
		t.Error("an unknown field passed")
	}

	cfgNew := cfg
	cfgNew.Services = func(context.Context, string) ([]string, error) { return []string{"db"}, nil }
	if _, err := composeCall(t, cfgNew, good); err == nil || !strings.Contains(err.Error(), "is new") {
		t.Errorf("new service: %v", err)
	}
	cfgGone := cfg
	cfgGone.Services = func(context.Context, string) ([]string, error) { return nil, nil }
	if _, err := composeCall(t, cfgGone, good); err == nil {
		t.Error("a project with no containers was updated")
	}
	// With no bus the unit does not start, and nothing is left behind.
	if _, err := composeCall(t, cfg, good); err == nil {
		t.Error("no bus, no error")
	}
	for _, f := range []string{cfg.File, cfg.Args} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", f, err)
		}
	}
}

func TestAddLabelsKeepTheProjectsOwnFiles(t *testing.T) {
	c, err := CheckCompose(composeOf(`,"labels":{"mine":"1"}`), "app")
	if err != nil {
		t.Fatal(err)
	}
	addLabels(c.Config, ComposeUpdateParams{Dir: "/d", Files: []string{"/d/a.yaml", "/d/b.yaml"}})
	labels := c.Config["services"].(map[string]any)["web"].(map[string]any)["labels"].(map[string]any)
	if labels[LabelComposeFiles] != "/d/a.yaml,/d/b.yaml" || labels[LabelComposeDir] != "/d" || labels["mine"] != "1" {
		t.Errorf("labels: %v", labels)
	}
}

func FuzzCheckCompose(f *testing.F) {
	f.Add(string(composeOf("")))
	f.Add(`{"name":"app","services":{"web":{"image":"nginx","privileged":true}}}`)
	f.Add(`{"name":"app","services":{"web":{"image":"nginx","volumes":[{"type":"bind","source":"/etc"}]}}}`)
	f.Fuzz(func(t *testing.T, s string) {
		c, err := CheckCompose([]byte(s), "app")
		if err != nil || len(c.Refused) > 0 {
			return
		}
		// Whatever passes has only listed keys and no risky bind.
		services := c.Config["services"].(map[string]any)
		for _, v := range services {
			svc := v.(map[string]any)
			for k := range svc {
				if !composeService[k] {
					t.Fatalf("key %q passed", k)
				}
			}
			vols, _ := svc["volumes"].([]any)
			for _, m := range vols {
				mount := m.(map[string]any)
				if src, _ := mount["source"].(string); mount["type"] == "bind" && riskyMount(src) {
					t.Fatalf("risky bind %q passed", src)
				}
			}
		}
	})
}
