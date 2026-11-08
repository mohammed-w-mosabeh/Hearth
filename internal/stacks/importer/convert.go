// Package importer turns a Compose project Hearth does not manage into a
// stack spec (ROADMAP 9.7 and 9.8). The project is rendered by Compose as
// the user, offline (no remote include or extends); Convert reads only that
// rendering and never prints it. Every environment value is a secret until
// the owner marks its name plain: the spec names it, its value goes to
// secrets.env at import and is never shown. Any value found elsewhere in
// the project (a password interpolated into a command) is masked and
// refused, since the spec would keep it in the clear.
package importer

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/secret"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/secrets"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Input is what Convert reads.
type Input struct {
	Project  string
	Rendered []byte          // docker compose config --format json
	Plain    map[string]bool // environment names the owner marked plain
	Skip     map[string]bool // services the owner leaves out
	TZ       string          // the host's time zone, for /etc/localtime binds
}

// SecretKey is one secret the import moves into secrets.env: its name and
// the services that receive it, never its value.
type SecretKey struct {
	Key      string   `json:"key"`
	Services []string `json:"services"`
}

// Result is a converted project.
type Result struct {
	Spec spec.Spec `json:"spec"`
	// Secrets are the names the import moves into secrets.env.
	Secrets []SecretKey `json:"secrets"`
	// Refused lists what stops the import; Changes what it alters.
	Refused []string `json:"refused"`
	Changes []string `json:"changes"`
	// Flags are the danger flags the project already uses; the proposed
	// spec opts in to them, and deploying it asks the danger dialog.
	Flags []string `json:"flags"`
	// Values are the secret values, for the import alone: never encoded.
	Values map[string]string `json:"-"`
}

type conv struct {
	in      Input
	r       Result
	values  map[string]string            // secret key -> value
	from    map[string]map[string]string // variable -> service -> value
	keyOf   map[string]map[string]string // service -> variable -> secret key
	keySvcs map[string]map[string]bool   // secret key -> services
	binds   map[string]bool
	project string
}

func (c *conv) refuse(where, format string, a ...any) {
	c.r.Refused = append(c.r.Refused, where+": "+fmt.Sprintf(format, a...))
}

func (c *conv) change(where, format string, a ...any) {
	c.r.Changes = append(c.r.Changes, where+": "+fmt.Sprintf(format, a...))
}

// Convert turns a rendering into a spec. It never returns a secret value
// except in Result.Values.
func Convert(in Input) Result {
	c := &conv{in: in, values: map[string]string{}, from: map[string]map[string]string{}, keyOf: map[string]map[string]string{},
		keySvcs: map[string]map[string]bool{}, binds: map[string]bool{}, project: in.Project}
	c.r.Spec = spec.Spec{Version: spec.Version, Name: in.Project, Services: map[string]spec.Service{}}
	if !spec.ValidName(in.Project) {
		c.refuse("project", "%q cannot be a stack name", clip(in.Project))
		return c.finish()
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(in.Rendered, &doc); err != nil {
		c.refuse("project", "Compose's rendering is not a JSON object")
		return c.finish()
	}
	for k := range doc {
		switch k {
		case "name", "services", "volumes", "networks":
		default:
			c.refuse("project", "top-level %q is not supported", clip(k))
		}
	}
	var services map[string]map[string]json.RawMessage
	if err := json.Unmarshal(doc["services"], &services); err != nil || len(services) == 0 {
		c.refuse("project", "no services")
		return c.finish()
	}
	// Environment first: every secret value is known before any other
	// field is read, so all of them can be masked.
	names := sortedKeys(services)
	for _, name := range names {
		if !c.in.Skip[name] {
			c.collectEnv(name, services[name])
		}
	}
	c.assignKeys()
	for _, name := range names {
		if c.in.Skip[name] {
			c.change(name, "left out, as asked")
			continue
		}
		c.service(name, services[name])
	}
	for name := range c.in.Skip {
		if _, ok := services[name]; !ok {
			c.refuse(name, "is not a service of the project")
		}
	}
	c.topVolumes(doc["volumes"])
	c.topNetworks(doc["networks"])
	c.secretsOneValue()
	if len(c.binds) > 0 {
		c.r.Spec.BindRoots = sortedKeys(c.binds)
		c.change("binds", "each bind source becomes an approved root: %s", strings.Join(c.r.Spec.BindRoots, ", "))
	}
	c.maskAll()
	return c.finish()
}

func (c *conv) finish() Result {
	r := c.r
	if len(r.Spec.Services) > 0 {
		rep := spec.Check(r.Spec)
		r.Flags = rep.Flags
		if len(rep.Flags) > 0 {
			// Propose what runs today; the owner opts in at import, behind
			// the danger dialog, or limits it first.
			r.Spec.OptIn = rep.Flags
			rep = spec.Check(r.Spec)
		}
		r.Refused = append(r.Refused, rep.Refused...)
	}
	keys := sortedKeys(c.values)
	r.Secrets = make([]SecretKey, 0, len(keys))
	for _, k := range keys {
		r.Secrets = append(r.Secrets, SecretKey{Key: k, Services: sortedKeys(c.keySvcs[k])})
	}
	r.Values = c.values
	sort.Strings(r.Refused)
	sort.Strings(r.Changes)
	if r.Refused == nil {
		r.Refused = []string{}
	}
	if r.Changes == nil {
		r.Changes = []string{}
	}
	if r.Flags == nil {
		r.Flags = []string{}
	}
	return r
}

// collectEnv records which environment values are secrets.
func (c *conv) collectEnv(name string, svc map[string]json.RawMessage) {
	var env map[string]*string
	if raw, ok := svc["environment"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &env); err != nil {
			c.refuse(name, "environment is not a map")
			return
		}
	}
	for k, v := range env {
		if c.in.Plain[k] || v == nil {
			continue
		}
		if c.from[k] == nil {
			c.from[k] = map[string]string{}
		}
		c.from[k][name] = *v
	}
}

// assignKeys picks each secret's key in secrets.env. A variable with one
// value everywhere keeps its name; one whose services hold different values
// gets a key per service; a name that cannot be a key (discovery.type) gets
// one that can. The container still sees its own variable name.
func (c *conv) assignKeys() {
	used := map[string]bool{}
	unique := func(base string) string {
		k := base
		for i := 2; used[k]; i++ {
			k = base + "_" + strconv.Itoa(i)
		}
		used[k] = true
		return k
	}
	assign := func(svc, variable, key, value string) {
		if c.keyOf[svc] == nil {
			c.keyOf[svc] = map[string]string{}
		}
		c.keyOf[svc][variable] = key
		c.values[key] = value
		if c.keySvcs[key] == nil {
			c.keySvcs[key] = map[string]bool{}
		}
		c.keySvcs[key][svc] = true
	}
	for _, variable := range sortedKeys(c.from) {
		bySvc := c.from[variable]
		distinct := map[string]bool{}
		for _, v := range bySvc {
			distinct[v] = true
		}
		if len(distinct) == 1 {
			key := unique(keyName(variable))
			if key != variable {
				c.change("secrets", "%s is kept as the secret %s", variable, key)
			}
			for svc, v := range bySvc {
				assign(svc, variable, key, v)
			}
			continue
		}
		for _, svc := range sortedKeys(bySvc) {
			key := unique(keyName(strings.ToUpper(svc + "_" + variable)))
			c.change(svc, "%s is kept as the secret %s, as other services hold another value", variable, key)
			assign(svc, variable, key, bySvc[svc])
		}
	}
}

// keyName makes a secrets.env key of s: upper case, _ for anything else,
// never a Compose or Docker setting.
func keyName(s string) string {
	if secrets.ValidKey(s) {
		return s
	}
	b := []byte(strings.ToUpper(s))
	for i, ch := range b {
		if (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' {
			b[i] = '_'
		}
	}
	k := string(b)
	if k == "" || (k[0] >= '0' && k[0] <= '9') || !secrets.ValidKey(k) {
		k = "S_" + k
	}
	if len(k) > 128 {
		k = k[:128]
	}
	return k
}

// secretsOneValue: secrets.env holds one value per name.
func (c *conv) secretsOneValue() {
	for _, k := range sortedKeys(c.values) {
		if why := secrets.CheckValue(c.values[k]); why != "" {
			c.refuse("secrets", "%s: %s", k, why)
		}
	}
	if len(c.values) > secrets.MaxKeys {
		c.refuse("secrets", "%d secrets, more than the %d a stack holds", len(c.values), secrets.MaxKeys)
	}
}

// text refuses and masks a string bound for the spec that holds a URL with
// user info (S19). Environment values found there are masked by maskAll.
func (c *conv) text(where, field, s string) string {
	if secret.Value(s) {
		c.refuse(where, "%s holds a URL with a user or password; pass it through the environment instead", field)
		return secret.Mask
	}
	return s
}

// minFree is the shortest secret value searched for in free text.
const minFree = 4

// maskAll masks the values S19 counts as secrets (by name, or a URL with
// user info) wherever they appear in the spec's free text and messages,
// and refuses the import by the secret's name: the spec would keep the
// value in the clear. Structural names (services, images, volumes, paths)
// are not searched: a value found there names something, as
// POSTGRES_USER=photos names the database, and hides nothing.
func (c *conv) maskAll() {
	names := c.structural()
	var hide []string
	keyOf := map[string]string{}
	for _, k := range sortedKeys(c.values) {
		v := c.values[k]
		if len(v) < minFree || (!secret.Name(k) && !secret.Value(v)) || within(names, v) {
			continue
		}
		hide = append(hide, v)
		keyOf[v] = k
	}
	if len(hide) == 0 {
		return
	}
	sort.Slice(hide, func(i, j int) bool { return len(hide[i]) > len(hide[j]) })
	hit := map[string]bool{}
	mask := func(s string) string {
		for _, v := range hide {
			if strings.Contains(s, v) {
				hit[keyOf[v]] = true
				s = strings.ReplaceAll(s, v, secret.Mask)
			}
		}
		return s
	}
	maskAll := func(l []string) {
		for i := range l {
			l[i] = mask(l[i])
		}
	}
	for name, svc := range c.r.Spec.Services {
		maskAll(svc.Command)
		maskAll(svc.Entrypoint)
		if svc.Healthcheck != nil {
			maskAll(svc.Healthcheck.Test)
		}
		for k, v := range svc.Labels {
			svc.Labels[k] = mask(v)
		}
		for k, v := range svc.ExtraHosts {
			svc.ExtraHosts[k] = mask(v)
		}
		for k, v := range svc.Sysctls {
			svc.Sysctls[k] = mask(v)
		}
		svc.User, svc.Hostname, svc.WorkingDir = mask(svc.User), mask(svc.Hostname), mask(svc.WorkingDir)
		c.r.Spec.Services[name] = svc
	}
	for _, k := range sortedKeys(hit) {
		c.refuse("secrets", "the value of %s also appears in the project outside its environment; pass it only through the environment", k)
	}
}

func (c *conv) texts(where, field string, l []string) []string {
	for i := range l {
		l[i] = c.text(where, field, l[i])
	}
	return l
}

// supported lists the service keys Convert reads; ignored are dropped
// with a change; anything else is refused by name.
var supported = map[string]bool{
	"image": true, "pull_policy": true, "container_name": true, "command": true, "entrypoint": true, "environment": true,
	"ports": true, "volumes": true, "tmpfs": true, "depends_on": true, "restart": true, "healthcheck": true, "user": true,
	"working_dir": true, "hostname": true, "labels": true, "networks": true, "network_mode": true, "pid": true, "ipc": true,
	"userns_mode": true, "cap_add": true, "cap_drop": true, "devices": true, "sysctls": true, "security_opt": true,
	"extra_hosts": true, "mem_limit": true, "cpus": true, "pids_limit": true, "shm_size": true, "read_only": true,
	"init": true, "stop_grace_period": true, "stop_signal": true, "build": true,
}

var ignored = map[string]string{
	"expose":      "expose is dropped: it only documents ports inside the stack's networks",
	"logging":     "logging is dropped: Docker's default driver is used",
	"platform":    "platform is dropped",
	"domainname":  "domainname is dropped",
	"mac_address": "mac_address is dropped",
}

func (c *conv) service(name string, raw map[string]json.RawMessage) {
	svc := spec.Service{}
	for k := range raw {
		switch {
		case supported[k]:
		case ignored[k] != "":
			c.change(name, "%s", ignored[k])
		default:
			c.refuse(name, "%s is not supported", clip(k))
		}
	}
	get := func(k string, v any) bool {
		b, ok := raw[k]
		if !ok || string(b) == "null" {
			return false
		}
		if err := json.Unmarshal(b, v); err != nil {
			c.refuse(name, "%s has a form Hearth does not read", k)
			return false
		}
		return true
	}
	str := func(k string) string {
		var s string
		get(k, &s)
		return s
	}
	svc.Image = str("image")
	svc.PullPolicy = str("pull_policy")
	if _, ok := raw["build"]; ok {
		if svc.Image == "" {
			c.refuse(name, "it is built from source and has no image; Hearth runs only images")
		} else {
			svc.PullPolicy = "never"
			c.change(name, "build is dropped: the local image %s is used, and never updated", svc.Image)
		}
	}
	svc.ContainerName = str("container_name")
	var l []string
	if get("command", &l) {
		svc.Command = c.texts(name, "command", l)
	}
	l = nil
	if get("entrypoint", &l) {
		svc.Entrypoint = c.texts(name, "entrypoint", l)
	}
	c.environment(name, raw, &svc)
	c.ports(name, raw, &svc)
	c.volumes(name, raw, &svc)
	var tmpfs any
	if get("tmpfs", &tmpfs) {
		switch t := tmpfs.(type) {
		case string:
			svc.Tmpfs = []string{t}
		case []any:
			for _, e := range t {
				if s, ok := e.(string); ok {
					svc.Tmpfs = append(svc.Tmpfs, s)
				}
			}
		}
	}
	var deps map[string]struct {
		Condition string `json:"condition"`
	}
	if get("depends_on", &deps) {
		for _, d := range sortedKeys(deps) {
			if c.in.Skip[d] {
				c.refuse(name, "depends on %s, which is left out", d)
				continue
			}
			svc.DependsOn = append(svc.DependsOn, d)
			if cond := deps[d].Condition; cond != "" && cond != "service_started" {
				c.change(name, "waits for %s to start, no longer until %s", d, strings.TrimPrefix(cond, "service_"))
			}
		}
	}
	svc.Restart = str("restart")
	if svc.Restart == "" {
		c.change(name, "restart becomes %s", spec.DefaultRestart)
	}
	c.healthcheck(name, raw, &svc)
	svc.User = c.text(name, "user", str("user"))
	svc.WorkingDir = str("working_dir")
	svc.Hostname = str("hostname")
	var labels map[string]string
	if get("labels", &labels) {
		svc.Labels = map[string]string{}
		for k, v := range labels {
			if strings.HasPrefix(k, "com.docker.compose.") {
				continue
			}
			svc.Labels[k] = c.text(name, "a label", v)
		}
		if len(svc.Labels) == 0 {
			svc.Labels = nil
		}
	}
	var nets map[string]*struct {
		Aliases []string `json:"aliases"`
	}
	if get("networks", &nets) {
		ns := sortedKeys(nets)
		if len(ns) != 1 || ns[0] != "default" {
			svc.Networks = ns
		}
		for _, n := range ns {
			if nets[n] != nil && len(nets[n].Aliases) > 0 {
				c.change(name, "its aliases on %s are dropped; other services reach it by its name", n)
			}
		}
	}
	svc.NetworkMode = str("network_mode")
	svc.PID = str("pid")
	svc.IPC = str("ipc")
	svc.UsernsMode = str("userns_mode")
	get("cap_add", &svc.CapAdd)
	get("cap_drop", &svc.CapDrop)
	var devices []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if get("devices", &devices) {
		for _, d := range devices {
			svc.Devices = append(svc.Devices, d.Source+":"+d.Target)
		}
	}
	get("sysctls", &svc.Sysctls)
	get("security_opt", &svc.SecurityOpt)
	var hosts any
	if get("extra_hosts", &hosts) {
		svc.ExtraHosts = map[string]string{}
		switch h := hosts.(type) {
		case map[string]any:
			for k, v := range h {
				svc.ExtraHosts[k] = fmt.Sprint(v)
			}
		case []any:
			for _, e := range h {
				k, v, _ := strings.Cut(fmt.Sprint(e), "=")
				svc.ExtraHosts[k] = v
			}
		}
	}
	svc.MemoryMB = c.megabytes(name, "mem_limit", str("mem_limit"))
	svc.ShmSizeMB = c.megabytes(name, "shm_size", str("shm_size"))
	var cpus any
	if get("cpus", &cpus) {
		svc.CPUs = fmt.Sprint(cpus)
	}
	get("pids_limit", &svc.PidsLimit)
	get("read_only", &svc.ReadOnly)
	get("init", &svc.Init)
	if s := str("stop_grace_period"); s != "" {
		svc.StopGraceS = c.seconds(name, "stop_grace_period", s)
	}
	svc.StopSignal = str("stop_signal")
	c.r.Spec.Services[name] = svc
}

func (c *conv) environment(name string, raw map[string]json.RawMessage, svc *spec.Service) {
	var env map[string]*string
	if b, ok := raw["environment"]; !ok || json.Unmarshal(b, &env) != nil {
		return
	}
	for _, k := range sortedKeys(env) {
		v := env[k]
		switch {
		case v == nil:
			c.change(name, "%s has no value and is dropped", k)
		case c.in.Plain[k]:
			if svc.Environment == nil {
				svc.Environment = map[string]string{}
			}
			svc.Environment[k] = *v
		default:
			if key := c.keyOf[name][k]; key != k {
				svc.Secrets = append(svc.Secrets, k+"="+key)
			} else {
				svc.Secrets = append(svc.Secrets, k)
			}
		}
	}
}

func (c *conv) ports(name string, raw map[string]json.RawMessage, svc *spec.Service) {
	var ports []struct {
		HostIP    string `json:"host_ip"`
		Target    int    `json:"target"`
		Published string `json:"published"`
		Protocol  string `json:"protocol"`
	}
	if b, ok := raw["ports"]; !ok || json.Unmarshal(b, &ports) != nil {
		return
	}
	for _, p := range ports {
		if p.Published == "" {
			c.change(name, "container port %d published on a random host port is dropped", p.Target)
			continue
		}
		host, err := strconv.Atoi(p.Published)
		if err != nil {
			c.refuse(name, "port range %q is not supported", clip(p.Published))
			continue
		}
		ip := p.HostIP
		if ip == "" {
			ip = "0.0.0.0"
			c.change(name, "port %d is open on every address today: keeping it needs an opt-in, or set it to 127.0.0.1", host)
		}
		svc.Ports = append(svc.Ports, spec.Port{HostIP: ip, Host: host, Container: p.Target, Protocol: p.Protocol})
	}
}

// timeFiles are binds of the host's time zone; S12b never allows /etc, so
// they become TZ (owner decision, 2026-11-02).
var timeFiles = map[string]bool{"/etc/localtime": true, "/etc/timezone": true}

func (c *conv) volumes(name string, raw map[string]json.RawMessage, svc *spec.Service) {
	var vols []struct {
		Type     string          `json:"type"`
		Source   string          `json:"source"`
		Target   string          `json:"target"`
		ReadOnly bool            `json:"read_only"`
		Bind     json.RawMessage `json:"bind"`
		Volume   json.RawMessage `json:"volume"`
	}
	if b, ok := raw["volumes"]; !ok || json.Unmarshal(b, &vols) != nil {
		return
	}
	tz := false
	for _, v := range vols {
		switch v.Type {
		case "volume":
			if v.Source == "" {
				c.change(name, "the anonymous volume at %s is dropped; its data is not kept", v.Target)
				continue
			}
			if len(v.Volume) > 0 && string(v.Volume) != "{}" {
				c.refuse(name, "volume options on %s are not supported", v.Target)
			}
			svc.Volumes = append(svc.Volumes, spec.Mount{Type: "volume", Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
		case "bind":
			if timeFiles[v.Source] {
				tz = true
				continue
			}
			if len(v.Bind) > 0 && string(v.Bind) != "{}" && string(v.Bind) != `{"create_host_path":true}` {
				c.refuse(name, "bind options on %s are not supported", v.Target)
			}
			c.binds[v.Source] = true
			svc.Volumes = append(svc.Volumes, spec.Mount{Type: "bind", Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
		case "tmpfs":
			svc.Tmpfs = append(svc.Tmpfs, v.Target)
		default:
			c.refuse(name, "a %s mount is not supported", clip(v.Type))
		}
	}
	if tz {
		if c.in.TZ == "" {
			c.change(name, "the host's time zone bind is dropped, and the host's zone is not known")
			return
		}
		if svc.Environment == nil {
			svc.Environment = map[string]string{}
		}
		if _, ok := svc.Environment["TZ"]; !ok && !hasSecretVar(svc.Secrets, "TZ") {
			svc.Environment["TZ"] = c.in.TZ
		}
		c.change(name, "the host's time zone bind becomes TZ=%s", c.in.TZ)
	}
}

func (c *conv) healthcheck(name string, raw map[string]json.RawMessage, svc *spec.Service) {
	var h struct {
		Test          []string `json:"test"`
		Interval      string   `json:"interval"`
		Timeout       string   `json:"timeout"`
		StartPeriod   string   `json:"start_period"`
		StartInterval string   `json:"start_interval"`
		Retries       *int     `json:"retries"`
		Disable       bool     `json:"disable"`
	}
	b, ok := raw["healthcheck"]
	if !ok || string(b) == "null" {
		return
	}
	if err := json.Unmarshal(b, &h); err != nil {
		c.refuse(name, "its healthcheck has a form Hearth does not read")
		return
	}
	if h.Disable || (len(h.Test) == 1 && h.Test[0] == "NONE") {
		svc.Healthcheck = &spec.Healthcheck{Disable: true}
		return
	}
	if len(h.Test) == 0 {
		// No test: the image's own healthcheck runs. The spec cannot
		// change its timings without a test, so they are dropped.
		if h.Interval != "" || h.Timeout != "" || h.StartPeriod != "" || h.Retries != nil {
			c.change(name, "its image's own healthcheck runs, with the image's timings")
		}
		return
	}
	hc := &spec.Healthcheck{Test: c.texts(name, "its healthcheck", h.Test)}
	if h.Interval != "" {
		hc.IntervalS = c.seconds(name, "healthcheck interval", h.Interval)
	}
	if h.Timeout != "" {
		hc.TimeoutS = c.seconds(name, "healthcheck timeout", h.Timeout)
	}
	if h.StartPeriod != "" {
		hc.StartPeriodS = c.seconds(name, "healthcheck start_period", h.StartPeriod)
	}
	if h.StartInterval != "" {
		c.change(name, "healthcheck start_interval is dropped")
	}
	if h.Retries != nil {
		hc.Retries = *h.Retries
	}
	svc.Healthcheck = hc
}

func (c *conv) seconds(name, field, s string) int {
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		c.refuse(name, "%s is not a duration", field)
		return 0
	}
	sec := int(math.Ceil(d.Seconds()))
	if time.Duration(sec)*time.Second != d {
		c.change(name, "%s is rounded up to %d s", field, sec)
	}
	return sec
}

func (c *conv) megabytes(name, field, s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		c.refuse(name, "%s is not a size", field)
		return 0
	}
	mb := int((n + (1<<20 - 1)) >> 20)
	if int64(mb)<<20 != n {
		c.change(name, "%s is rounded up to %d MiB", field, mb)
	}
	return mb
}

func (c *conv) topVolumes(raw json.RawMessage) {
	var vols map[string]struct {
		Name       string          `json:"name"`
		External   bool            `json:"external"`
		Driver     string          `json:"driver"`
		DriverOpts json.RawMessage `json:"driver_opts"`
		Labels     json.RawMessage `json:"labels"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &vols) != nil {
		return
	}
	for _, k := range sortedKeys(vols) {
		v := vols[k]
		if v.Driver != "" && v.Driver != "local" {
			c.refuse("volume "+k, "driver %s is not supported", clip(v.Driver))
		}
		if len(v.DriverOpts) > 0 && string(v.DriverOpts) != "{}" && string(v.DriverOpts) != "null" {
			c.refuse("volume "+k, "driver_opts are not supported")
		}
		if len(v.Labels) > 0 && string(v.Labels) != "{}" && string(v.Labels) != "null" {
			c.change("volume "+k, "its labels are dropped")
		}
		sv := spec.Volume{External: v.External}
		if v.Name != "" && v.Name != c.project+"_"+k {
			sv.Name = v.Name
		}
		if c.r.Spec.Volumes == nil {
			c.r.Spec.Volumes = map[string]spec.Volume{}
		}
		c.r.Spec.Volumes[k] = sv
	}
}

func (c *conv) topNetworks(raw json.RawMessage) {
	var nets map[string]struct {
		Name     string          `json:"name"`
		External bool            `json:"external"`
		Internal bool            `json:"internal"`
		Driver   string          `json:"driver"`
		IPAM     json.RawMessage `json:"ipam"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &nets) != nil {
		return
	}
	used := map[string]bool{}
	for _, s := range c.r.Spec.Services {
		for _, n := range s.Networks {
			used[n] = true
		}
	}
	for _, k := range sortedKeys(nets) {
		n := nets[k]
		if k == "default" && !used["default"] && (n.Name == "" || n.Name == c.project+"_default") {
			continue // Compose makes it by itself
		}
		if n.Driver != "" && n.Driver != "bridge" {
			c.refuse("network "+k, "driver %s is not supported", clip(n.Driver))
		}
		if len(n.IPAM) > 0 && string(n.IPAM) != "{}" && string(n.IPAM) != "null" {
			c.refuse("network "+k, "ipam settings are not supported")
		}
		sn := spec.Network{External: n.External, Internal: n.Internal}
		if n.Name != "" && n.Name != c.project+"_"+k {
			sn.Name = n.Name
		}
		if c.r.Spec.Networks == nil {
			c.r.Spec.Networks = map[string]spec.Network{}
		}
		c.r.Spec.Networks[k] = sn
	}
}

// structural lists the project's names: services, containers, volumes,
// networks and images. A value found within one hides nothing.
func (c *conv) structural() []string {
	out := []string{c.project}
	for name, s := range c.r.Spec.Services {
		out = append(out, name, s.ContainerName, s.Image)
	}
	for k, v := range c.r.Spec.Volumes {
		out = append(out, k, v.Name)
	}
	for k, n := range c.r.Spec.Networks {
		out = append(out, k, n.Name)
	}
	return out
}

func within(l []string, v string) bool {
	for _, s := range l {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasSecretVar(l []string, variable string) bool {
	for _, e := range l {
		if v, _ := spec.SecretRef(e); v == variable {
			return true
		}
	}
	return false
}

func clip(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.ToValidUTF8(s, "?")
}
