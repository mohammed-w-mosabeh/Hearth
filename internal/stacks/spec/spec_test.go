package spec

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// good is a realistic stack that passes with two opt-ins.
const good = `{
  "version": 1,
  "name": "photos",
  "services": {
    "server": {
      "image": "ghcr.io/photos-app/photos-server:v1.120.0",
      "command": ["start.sh", "$HOME", "${NOT_INTERPOLATED}"],
      "environment": {"TZ": "Europe/Berlin", "DB_HOST": "db"},
      "secrets": ["DB_PASSWORD"],
      "ports": [{"host": 2283, "container": 2283}, {"host_ip": "0.0.0.0", "host": 8443, "container": 443}],
      "volumes": [
        {"type": "bind", "source": "/home/me/Pictures", "target": "/photos", "read_only": true},
        {"type": "volume", "source": "upload", "target": "/usr/src/app/upload"}
      ],
      "devices": ["/dev/dri"],
      "depends_on": ["db"],
      "restart": "unless-stopped",
      "healthcheck": {"test": ["CMD", "curl", "-f", "http://localhost:2283/api/server/ping"], "interval_s": 30, "retries": 3},
      "labels": {"traefik.enable": "false"},
      "extra_hosts": {"host.docker.internal": "host-gateway"},
      "memory_mb": 4096, "cpus": "2.5", "security_opt": ["no-new-privileges:true"]
    },
    "db": {
      "image": "docker.io/tensorchord/pgvecto-rs:pg14-v0.2.0@sha256:90724186f0a3517cf6914295b5ab410db9ce23190a2d9d0b9dd6463e3fa298f0",
      "secrets": ["DB_PASSWORD"],
      "volumes": [{"type": "volume", "source": "pgdata", "target": "/var/lib/postgresql/data"}],
      "networks": ["default", "back"]
    }
  },
  "volumes": {"upload": {}, "pgdata": {"name": "photos_pgdata", "external": true}},
  "networks": {"back": {"internal": true}},
  "bind_roots": ["/home/me/Pictures"],
  "opt_in": ["device:/dev/dri", "port:0.0.0.0:8443/tcp"]
}`

func TestGoodSpec(t *testing.T) {
	s, r, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() {
		t.Fatalf("refused: %q", r.Refused)
	}
	if want := []string{"device:/dev/dri", "port:0.0.0.0:8443/tcp"}; !slices.Equal(r.Flags, want) {
		t.Fatalf("flags %q", r.Flags)
	}
	// "$" stays text: the spec has no interpolation, and 9.2 escapes it.
	if got := s.Services["server"].Command; got[1] != "$HOME" || got[2] != "${NOT_INTERPOLATED}" {
		t.Fatalf("command %q", got)
	}
	// A decoded spec marshals back to one that reads the same.
	b, _ := json.Marshal(s)
	s2, r2, err := Parse(b)
	if err != nil || !r2.OK() {
		t.Fatalf("round trip: %v %q", err, r2.Refused)
	}
	b2, _ := json.Marshal(s2)
	if string(b) != string(b2) {
		t.Fatal("round trip changed the spec")
	}
}

// edit returns good with one JSON value replaced, by path.
func edit(t *testing.T, change func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(good), &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func svc(m map[string]any, name string) map[string]any {
	return m["services"].(map[string]any)[name].(map[string]any)
}

func TestDecodeRefuses(t *testing.T) {
	cases := map[string]string{
		"yaml":            "version: 1\nname: x\nservices:\n  a:\n    image: nginx\n",
		"yaml anchor":     "x: &a {image: nginx}\nservices: {a: *a}",
		"two values":      `{"version":1} {"version":1}`,
		"trailing":        `{"version":1} x`,
		"empty":           ``,
		"array":           `[]`,
		"null":            `{"version":1,"name":null}`,
		"duplicate":       `{"version":1,"version":1}`,
		"duplicate deep":  `{"services":{"a":{"image":"x","image":"y"}}}`,
		"duplicate map":   `{"services":{"a":{"environment":{"A":"1","A":"2"}}}}`,
		"case variant":    `{"Version":1}`,
		"case in service": `{"services":{"a":{"IMAGE":"nginx"}}}`,
		"bad utf8":        "{\"name\":\"\xff\"}",
		"wrong type":      `{"version":"1"}`,
		"deep":            `{"services":{"a":{"healthcheck":{"test":[[[[["x"]]]]]}}}}`,
	}
	for _, k := range []string{"privileged", "build", "env_file", "extends", "profiles", "provider", "post_start", "pre_stop",
		"volumes_from", "cgroup_parent", "credential_spec", "logging", "x-hearth", "configs", "secrets_from", "develop", "links"} {
		cases["service key "+k] = `{"services":{"a":{"image":"nginx","` + k + `":true}}}`
	}
	for _, k := range []string{"include", "x-common", "configs", "secrets", "extends", "services_extra"} {
		cases["top key "+k] = `{"version":1,"` + k + `":{}}`
	}
	for where, j := range map[string]string{
		"port":        `{"services":{"a":{"ports":[{"host":1,"container":1,"mode":"host"}]}}}`,
		"mount":       `{"services":{"a":{"volumes":[{"type":"bind","source":"/x","target":"/y","bind":{"propagation":"rshared"}}]}}}`,
		"healthcheck": `{"services":{"a":{"healthcheck":{"test":["NONE"],"shell":true}}}}`,
		"volume":      `{"volumes":{"v":{"driver":"local","driver_opts":{"type":"none","o":"bind","device":"/etc"}}}}`,
		"network":     `{"networks":{"n":{"driver":"macvlan"}}}`,
	} {
		cases["unknown key in "+where] = j
	}
	big := `{"version":1,"name":"` + strings.Repeat("a", MaxSpec) + `"}`
	cases["1 MiB"] = `{"name":"` + strings.Repeat("a", 1<<20) + `"}`
	cases["over MaxSpec"] = big
	cases["nested 10000"] = strings.Repeat("[", 10000) + strings.Repeat("]", 10000)
	for name, j := range cases {
		if _, err := Decode([]byte(j)); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
}

func TestCheckRefuses(t *testing.T) {
	type change = func(m map[string]any)
	setSvc := func(k string, v any) change { return func(m map[string]any) { svc(m, "server")[k] = v } }
	bind := func(src string) change {
		return setSvc("volumes", []any{map[string]any{"type": "bind", "source": src, "target": "/x"}})
	}
	cases := map[string]change{
		"version 2":             func(m map[string]any) { m["version"] = 2 },
		"name ../x":             func(m map[string]any) { m["name"] = "../x" },
		"name -x":               func(m map[string]any) { m["name"] = "-x" },
		"name upper":            func(m map[string]any) { m["name"] = "Photos" },
		"name cyrillic a":       func(m map[string]any) { m["name"] = "phоtos" },
		"name too long":         func(m map[string]any) { m["name"] = strings.Repeat("a", 41) },
		"service ../x":          func(m map[string]any) { m["services"].(map[string]any)["../x"] = map[string]any{"image": "nginx"} },
		"no services":           func(m map[string]any) { m["services"] = map[string]any{} },
		"bad image":             setSvc("image", "-v /:/host nginx"),
		"image with space":      setSvc("image", "nginx latest"),
		"pull daily":            setSvc("pull_policy", "daily"),
		"restart sometimes":     setSvc("restart", "sometimes"),
		"bind /":                bind("/"),
		"bind /etc":             bind("/etc"),
		"bind under /etc":       bind("/etc/ssl"),
		"bind /var/lib/hearth":  bind("/var/lib/hearth/stacks"),
		"bind /var/lib/docker":  bind("/var/lib/docker"),
		"bind traversal":        bind("/home/me/Pictures/../../../etc"),
		"bind trailing slash":   bind("/home/me/Pictures/"),
		"bind dot":              bind("/home/me/Pictures/."),
		"bind relative":         bind("Pictures"),
		"bind proc self root":   bind("/proc/self/root"),
		"bind proc 1 root":      bind("/proc/1/root/etc"),
		"bind proc 1":           bind("/proc/1"),
		"bind raw disk":         bind("/dev/sda"),
		"bind /dev/mem":         bind("/dev/mem"),
		"bind outside roots":    bind("/home/me/.ssh"),
		"bind root prefix":      bind("/home/me/PicturesX"),
		"bind control char":     bind("/home/me/Pictures/a\nb"),
		"undeclared volume":     setSvc("volumes", []any{map[string]any{"type": "volume", "source": "nope", "target": "/x"}}),
		"mount type tmpfs":      setSvc("volumes", []any{map[string]any{"type": "tmpfs", "source": "", "target": "/x"}}),
		"mount target /":        setSvc("volumes", []any{map[string]any{"type": "volume", "source": "upload", "target": "/"}}),
		"cap ALL":               setSvc("cap_add", []any{"ALL"}),
		"cap SYS_ADMIN":         setSvc("cap_add", []any{"SYS_ADMIN"}),
		"cap CAP_SYS_MODULE":    setSvc("cap_add", []any{"CAP_SYS_MODULE"}),
		"cap lower":             setSvc("cap_add", []any{"net_admin"}),
		"device /dev/mem":       setSvc("devices", []any{"/dev/mem"}),
		"device /dev/kmem":      setSvc("devices", []any{"/dev/kmem"}),
		"device raw disk":       setSvc("devices", []any{"/dev/nvme0n1"}),
		"device mapper":         setSvc("devices", []any{"/dev/mapper/root"}),
		"device unknown":        setSvc("devices", []any{"/dev/watchdog"}),
		"device traversal":      setSvc("devices", []any{"/dev/dri/../sda"}),
		"seccomp unconfined":    setSvc("security_opt", []any{"seccomp=unconfined"}),
		"apparmor unconfined":   setSvc("security_opt", []any{"apparmor:unconfined"}),
		"label disable":         setSvc("security_opt", []any{"label=disable"}),
		"sysctl not namespaced": setSvc("sysctls", map[string]any{"kernel.panic": "1"}),
		"network_mode bridge2":  setSvc("network_mode", "container:abc"),
		"network_mode self":     setSvc("network_mode", "service:server"),
		"pid container":         setSvc("pid", "container:abc"),
		"ipc container":         setSvc("ipc", "container:abc"),
		"userns other":          setSvc("userns_mode", "keep-id"),
		"reserved label":        setSvc("labels", map[string]any{"com.docker.compose.project": "other"}),
		"hearth label":          setSvc("labels", map[string]any{"org.hearth.compose.dir": "/etc"}),
		"env name":              setSvc("environment", map[string]any{"A-B": "1"}),
		"env NUL":               setSvc("environment", map[string]any{"A": "x\x00y"}),
		"secret twice":          setSvc("secrets", []any{"A", "A"}),
		"secret and plain":      setSvc("secrets", []any{"TZ"}),
		"port 0":                setSvc("ports", []any{map[string]any{"host": 0, "container": 80}}),
		"port 70000":            setSvc("ports", []any{map[string]any{"host": 70000, "container": 80}}),
		"port proto":            setSvc("ports", []any{map[string]any{"host": 80, "container": 80, "protocol": "sctp"}}),
		"port ip zone":          setSvc("ports", []any{map[string]any{"host_ip": "fe80::1%eth0", "host": 80, "container": 80}}),
		"port ip text":          setSvc("ports", []any{map[string]any{"host_ip": "localhost", "host": 80, "container": 80}}),
		"port twice": setSvc("ports", []any{map[string]any{"host": 80, "container": 80},
			map[string]any{"host_ip": "127.0.0.1", "host": 80, "container": 81}}),
		"depends on self":    setSvc("depends_on", []any{"server"}),
		"depends on unknown": setSvc("depends_on", []any{"nope"}),
		"depends cycle":      func(m map[string]any) { svc(m, "db")["depends_on"] = []any{"server"} },
		"health bad":         setSvc("healthcheck", map[string]any{"test": []any{"sh", "-c", "x"}}),
		"user bad":           setSvc("user", "root;id"),
		"working_dir rel":    setSvc("working_dir", "app"),
		"hostname bad":       setSvc("hostname", "-bad"),
		"cpus bad":           setSvc("cpus", "1e3"),
		"memory negative":    setSvc("memory_mb", -1),
		"extra host bad":     setSvc("extra_hosts", map[string]any{"a": "not-an-ip"}),
		"root /":             func(m map[string]any) { m["bind_roots"] = []any{"/"} },
		"root /etc":          func(m map[string]any) { m["bind_roots"] = []any{"/etc"} },
		"root /root":         func(m map[string]any) { m["bind_roots"] = []any{"/root"} },
		"root /proc":         func(m map[string]any) { m["bind_roots"] = []any{"/proc"} },
		"root traversal":     func(m map[string]any) { m["bind_roots"] = []any{"/home/../etc"} },
		"root docker sock":   func(m map[string]any) { m["bind_roots"] = []any{"/var/run/docker.sock"} },
		"opt-in unused":      func(m map[string]any) { m["opt_in"] = append(m["opt_in"].([]any), "cap:NET_ADMIN") },
		"opt-in twice":       func(m map[string]any) { m["opt_in"] = append(m["opt_in"].([]any), "device:/dev/dri") },
		"opt-in missing":     func(m map[string]any) { m["opt_in"] = []any{"device:/dev/dri"} },
		"opt-in none":        func(m map[string]any) { delete(m, "opt_in") },
	}
	for name, ch := range cases {
		_, r, err := Parse(edit(t, ch))
		if err == nil && r.OK() {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Every flag S12b allows with an opt-in: refused without it, accepted with it.
func TestFlagsNeedOptIn(t *testing.T) {
	cases := map[string]struct {
		key  string
		val  any
		flag string
	}{
		"cap":         {"cap_add", []any{"NET_ADMIN"}, "cap:NET_ADMIN"},
		"cap prefix":  {"cap_add", []any{"CAP_NET_RAW"}, "cap:NET_RAW"},
		"gpu":         {"devices", []any{"/dev/dri/renderD128:/dev/dri/renderD128"}, "device:/dev/dri/renderD128"},
		"tun":         {"devices", []any{"/dev/net/tun"}, "device:/dev/net/tun"},
		"host net":    {"network_mode", "host", "network:host"},
		"host pid":    {"pid", "host", "pid:host"},
		"host ipc":    {"ipc", "host", "ipc:host"},
		"userns host": {"userns_mode", "host", "userns:host"},
		"sysctl":      {"sysctls", map[string]any{"net.ipv4.ip_forward": "1"}, "sysctl:net.ipv4.ip_forward"},
		"socket":      {"volumes", []any{map[string]any{"type": "bind", "source": "/var/run/docker.sock", "target": "/s"}}, "docker-socket"},
		"run socket":  {"volumes", []any{map[string]any{"type": "bind", "source": "/run/docker.sock", "target": "/s"}}, "docker-socket"},
		"bind root":   {"volumes", []any{map[string]any{"type": "bind", "source": "/root", "target": "/r"}}, "bind:/root"},
		"bind proc":   {"volumes", []any{map[string]any{"type": "bind", "source": "/proc", "target": "/p"}}, "bind:/proc"},
		"bind sys":    {"volumes", []any{map[string]any{"type": "bind", "source": "/sys/class/power_supply", "target": "/p"}}, "bind:/sys/class/power_supply"},
		"bind dev":    {"volumes", []any{map[string]any{"type": "bind", "source": "/dev/bus/usb", "target": "/u"}}, "bind:/dev/bus/usb"},
		"lan port":    {"ports", []any{map[string]any{"host_ip": "192.168.1.10", "host": 80, "container": 80}}, "port:192.168.1.10:80/tcp"},
		"v6 port":     {"ports", []any{map[string]any{"host_ip": "::", "host": 80, "container": 80, "protocol": "udp"}}, "port:[::]:80/udp"},
	}
	for name, tc := range cases {
		base := func(m map[string]any) {
			s := svc(m, "server")
			delete(s, "ports") // a network_mode allows no ports
			delete(s, "volumes")
			delete(s, "devices")
			m["opt_in"] = []any{}
			m["volumes"] = map[string]any{"pgdata": map[string]any{}}
			s[tc.key] = tc.val
		}
		_, r, err := Parse(edit(t, base))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.OK() || !slices.Contains(r.Flags, tc.flag) {
			t.Errorf("%s: without opt-in: refused %q flags %q", name, r.Refused, r.Flags)
		}
		_, r, _ = Parse(edit(t, func(m map[string]any) { base(m); m["opt_in"] = []any{tc.flag} }))
		if !r.OK() {
			t.Errorf("%s: with opt-in %s: %q", name, tc.flag, r.Refused)
		}
	}
}

func TestLoopbackIsNotAFlag(t *testing.T) {
	_, r, _ := Parse(edit(t, func(m map[string]any) {
		s := svc(m, "server")
		s["ports"] = []any{map[string]any{"host": 80, "container": 80}, map[string]any{"host_ip": "::1", "host": 80, "container": 80}}
		m["opt_in"] = []any{"device:/dev/dri"}
	}))
	if !r.OK() || slices.ContainsFunc(r.Flags, func(f string) bool { return strings.HasPrefix(f, "port:") }) {
		t.Fatalf("%q %q", r.Refused, r.Flags)
	}
}

func TestMessagesAreBounded(t *testing.T) {
	_, err := Decode([]byte(`{"` + strings.Repeat("k", 5000) + `\u0007":1}`))
	if err == nil || len(err.Error()) > 120 || strings.ContainsRune(err.Error(), 7) {
		t.Fatalf("%v", err)
	}
}
