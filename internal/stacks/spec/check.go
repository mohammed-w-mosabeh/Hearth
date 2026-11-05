package spec

import (
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/imageref"
)

// Bounds, generous for real stacks and small enough to show in full.
const (
	maxServices = 32
	maxList     = 64  // entries of any list in a service
	maxEnv      = 128 // environment entries in a service
	maxSecrets  = 64  // secret keys in a service
	maxVolumes  = 64  // top-level volumes or networks
	maxRoots    = 16
	maxOptIn    = 64
	maxArg      = 4096 // one command argument or environment value
	maxPathLen  = 1024
	maxLabel    = 1024
)

var (
	// A stack name is also a Compose project, a unit instance and a folder:
	// lower-case ASCII, so no lookalike, no leading "-", "_" or ".".
	stackName   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	objectName  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	envKey      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	// plainEnvKey also takes the dotted names some images read (Elasticsearch's
	// discovery.type): a plain value is written escaped, so its name is never
	// interpolated. A secret's name stays envKey, as it becomes ${KEY}.
	plainEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	labelKey    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$`)
	userSpec    = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,31}(?::[a-z0-9_][a-z0-9_.-]{0,31})?$`)
	hostName    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)
	capName     = regexp.MustCompile(`^[A-Z][A-Z_]{1,31}$`)
	cpus        = regexp.MustCompile(`^(?:[1-9][0-9]{0,2}|0)(?:\.[0-9]{1,2})?$`)
	signal      = regexp.MustCompile(`^SIG[A-Z0-9]{2,8}$`)
	// Namespaced sysctls only: Docker refuses the others anyway.
	sysctlName = regexp.MustCompile(`^(?:net\.[a-z0-9_.-]{1,96}|kernel\.(?:shm[a-z_]{1,16}|msg[a-z_]{1,16}|sem)|fs\.mqueue\.[a-z_]{1,32})$`)
	sysctlVal  = regexp.MustCompile(`^[0-9a-zA-Z ._:-]{1,64}$`)
)

// Report is what Check finds.
type Report struct {
	// Refused lists what is not allowed, as "where: why", sorted. A spec
	// may be used only when it is empty.
	Refused []string `json:"refused"`
	// Flags lists the danger flags the spec uses, sorted; each needs an
	// opt-in, and deploying one needs org.hearth.stack.danger.
	Flags []string `json:"flags"`
	// ServiceFlags lists, per service, the flags it carries, sorted: the
	// danger audit names them per container.
	ServiceFlags map[string][]string `json:"service_flags,omitempty"`
}

// OK reports whether nothing was refused.
func (r Report) OK() bool { return len(r.Refused) == 0 }

// Parse decodes and checks a spec.
func Parse(raw []byte) (Spec, Report, error) {
	s, err := Decode(raw)
	if err != nil {
		return s, Report{}, err
	}
	return s, Check(s), nil
}

type checker struct {
	s        Spec
	r        Report
	flags    map[string]bool
	svcFlags map[string]map[string]bool
	ports    map[string]string // "ip:port/proto" -> service
}

func (c *checker) refuse(where, format string, a ...any) {
	c.r.Refused = append(c.r.Refused, where+": "+fmt.Sprintf(format, a...))
}

func (c *checker) flag(service, f string) {
	c.flags[f] = true
	if c.svcFlags[service] == nil {
		c.svcFlags[service] = map[string]bool{}
	}
	c.svcFlags[service][f] = true
}

// Check applies S12b to a decoded spec.
func Check(s Spec) Report {
	c := &checker{s: s, flags: map[string]bool{}, svcFlags: map[string]map[string]bool{}, ports: map[string]string{}}
	if s.Version != Version {
		c.refuse("spec", "version %d is not %d", s.Version, Version)
	}
	if !stackName.MatchString(s.Name) {
		c.refuse("spec", "the name must be 1 to 40 of a-z, 0-9, - and _, starting with a letter or digit")
	}
	if len(s.Services) == 0 || len(s.Services) > maxServices {
		c.refuse("spec", "a stack has 1 to %d services", maxServices)
	}
	if len(s.Volumes) > maxVolumes || len(s.Networks) > maxVolumes {
		c.refuse("spec", "at most %d volumes and %d networks", maxVolumes, maxVolumes)
	}
	c.roots()
	for name, v := range s.Volumes {
		if !objectName.MatchString(name) || (v.Name != "" && !objectName.MatchString(v.Name)) {
			c.refuse("volume "+clip(name), "not a volume name")
		}
	}
	for name, n := range s.Networks {
		if !objectName.MatchString(name) || (n.Name != "" && !objectName.MatchString(n.Name)) {
			c.refuse("network "+clip(name), "not a network name")
		}
	}
	names := make([]string, 0, len(s.Services))
	for name := range s.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	containers := map[string]bool{}
	for _, name := range names {
		if !serviceName.MatchString(name) {
			c.refuse("service "+clip(name), "not a service name")
			continue
		}
		svc := s.Services[name]
		if svc.ContainerName != "" {
			if containers[svc.ContainerName] {
				c.refuse(name, "container_name is used twice")
			}
			containers[svc.ContainerName] = true
		}
		c.service(name, svc)
	}
	c.dependencies(names)
	c.optIns()
	sort.Strings(c.r.Refused)
	c.r.Refused = slices.Compact(c.r.Refused)
	c.r.Flags = make([]string, 0, len(c.flags))
	for f := range c.flags {
		c.r.Flags = append(c.r.Flags, f)
	}
	sort.Strings(c.r.Flags)
	for svc, fs := range c.svcFlags {
		if c.r.ServiceFlags == nil {
			c.r.ServiceFlags = map[string][]string{}
		}
		for f := range fs {
			c.r.ServiceFlags[svc] = append(c.r.ServiceFlags[svc], f)
		}
		sort.Strings(c.r.ServiceFlags[svc])
	}
	if c.r.Refused == nil {
		c.r.Refused = []string{}
	}
	return c.r
}

func (c *checker) roots() {
	if len(c.s.BindRoots) > maxRoots {
		c.refuse("bind_roots", "at most %d", maxRoots)
	}
	for _, r := range c.s.BindRoots {
		switch {
		case !cleanAbs(r):
			c.refuse("bind_roots", "%q is not an absolute, clean path", clip(r))
		case r == "/":
			c.refuse("bind_roots", "/ cannot be a root")
		case neverBind(r) != "" || hostTree(r) != "" || dockerSocket(r):
			c.refuse("bind_roots", "%s cannot be a root", r)
		}
	}
	if len(c.s.OptIn) > maxOptIn {
		c.refuse("opt_in", "at most %d", maxOptIn)
	}
}

// optIns refuses a flag without its opt-in and an opt-in without its flag,
// so the opt-ins say exactly what the stack may do.
func (c *checker) optIns() {
	seen := map[string]bool{}
	for _, o := range c.s.OptIn {
		if seen[o] {
			c.refuse("opt_in", "%q is listed twice", clip(o))
		}
		seen[o] = true
		if !c.flags[o] {
			c.refuse("opt_in", "%q is not used by the stack", clip(o))
		}
	}
	for f := range c.flags {
		if !seen[f] {
			c.refuse("opt_in", "%s needs an opt-in", f)
		}
	}
}

func (c *checker) service(name string, svc Service) {
	if _, _, _, ok := imageref.Parse(svc.Image); !ok {
		c.refuse(name, "the image is not a reference")
	}
	switch svc.PullPolicy {
	case "", "always", "missing", "never":
	default:
		c.refuse(name, "pull_policy must be always, missing or never")
	}
	if svc.ContainerName != "" && !objectName.MatchString(svc.ContainerName) {
		c.refuse(name, "not a container name")
	}
	c.args(name, "command", svc.Command)
	c.args(name, "entrypoint", svc.Entrypoint)
	c.env(name, svc)
	c.portList(name, svc.Ports)
	c.mounts(name, svc)
	switch svc.Restart {
	case "", "no", "always", "unless-stopped", "on-failure":
	default:
		c.refuse(name, "restart must be no, always, unless-stopped or on-failure")
	}
	c.health(name, svc.Healthcheck)
	if svc.User != "" && !userSpec.MatchString(svc.User) {
		c.refuse(name, "user must be a name or id, optionally :group")
	}
	if svc.WorkingDir != "" && !cleanAbs(svc.WorkingDir) {
		c.refuse(name, "working_dir must be an absolute, clean path")
	}
	if svc.Hostname != "" && (len(svc.Hostname) > 253 || !hostName.MatchString(svc.Hostname)) {
		c.refuse(name, "not a host name")
	}
	if len(svc.Labels) > maxList {
		c.refuse(name, "at most %d labels", maxList)
	}
	for k, v := range svc.Labels {
		if !labelKey.MatchString(k) || len(v) > maxLabel || !printable(v) {
			c.refuse(name, "label %q is not allowed", clip(k))
		} else if reservedLabel(k) {
			c.refuse(name, "label %s is reserved", k)
		}
	}
	c.networking(name, svc)
	c.capabilities(name, svc)
	c.deviceList(name, svc.Devices)
	c.sysctls(name, svc.Sysctls)
	for _, o := range svc.SecurityOpt {
		if o != "no-new-privileges" && o != "no-new-privileges:true" {
			c.refuse(name, "security_opt %q is not allowed", clip(o))
		}
	}
	if len(svc.SecurityOpt) > 1 {
		c.refuse(name, "security_opt is listed twice")
	}
	if len(svc.ExtraHosts) > maxList {
		c.refuse(name, "at most %d extra_hosts", maxList)
	}
	for h, ip := range svc.ExtraHosts {
		if len(h) > 253 || !hostName.MatchString(h) {
			c.refuse(name, "extra_hosts: %q is not a host name", clip(h))
		}
		if _, err := netip.ParseAddr(ip); (err != nil || strings.Contains(ip, "%")) && ip != "host-gateway" {
			c.refuse(name, "extra_hosts: %q is not an address", clip(ip))
		}
	}
	if svc.MemoryMB < 0 || svc.MemoryMB > 1<<20 || svc.ShmSizeMB < 0 || svc.ShmSizeMB > 1<<16 {
		c.refuse(name, "memory_mb or shm_size_mb is out of range")
	}
	if svc.CPUs != "" && !cpus.MatchString(svc.CPUs) {
		c.refuse(name, "cpus must be a number such as 1.5")
	}
	if svc.PidsLimit < 0 || svc.PidsLimit > 1<<20 {
		c.refuse(name, "pids_limit is out of range")
	}
	if svc.StopGraceS < 0 || svc.StopGraceS > 3600 {
		c.refuse(name, "stop_grace_period_s is 0 to 3600")
	}
	if svc.StopSignal != "" && !signal.MatchString(svc.StopSignal) {
		c.refuse(name, "stop_signal must be a signal name such as SIGTERM")
	}
}

func (c *checker) args(name, what string, a []string) {
	if len(a) > maxList {
		c.refuse(name, "%s has more than %d arguments", what, maxList)
	}
	for _, s := range a {
		if len(s) > maxArg || strings.ContainsRune(s, 0) {
			c.refuse(name, "%s has an argument that is too long or holds NUL", what)
		}
	}
}

func (c *checker) env(name string, svc Service) {
	if len(svc.Environment) > maxEnv {
		c.refuse(name, "at most %d environment entries", maxEnv)
	}
	for k, v := range svc.Environment {
		if !plainEnvKey.MatchString(k) {
			c.refuse(name, "environment: %q is not a variable name", clip(k))
		}
		if len(v) > maxArg || strings.ContainsRune(v, 0) {
			c.refuse(name, "environment: %s is too long or holds NUL", clip(k))
		}
	}
	if len(svc.Secrets) > maxSecrets {
		c.refuse(name, "at most %d secrets", maxSecrets)
	}
	seen := map[string]bool{}
	for _, k := range svc.Secrets {
		switch {
		case !envKey.MatchString(k):
			c.refuse(name, "secrets: %q is not a variable name", clip(k))
		case composeSetting(k):
			c.refuse(name, "secrets: %s is a Compose or Docker setting, not a secret name", k)
		case seen[k]:
			c.refuse(name, "secrets: %s is listed twice", k)
		default:
			if _, ok := svc.Environment[k]; ok {
				c.refuse(name, "%s is both a secret and a plain value", k)
			}
		}
		seen[k] = true
	}
}

func (c *checker) portList(name string, ports []Port) {
	if len(ports) > maxList {
		c.refuse(name, "at most %d ports", maxList)
	}
	for _, p := range ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			c.refuse(name, "a port's protocol is tcp or udp")
			continue
		}
		if p.Host < 1 || p.Host > 65535 || p.Container < 1 || p.Container > 65535 {
			c.refuse(name, "ports are 1 to 65535")
			continue
		}
		ip := p.HostIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil || addr.Zone() != "" || addr.String() != ip {
			c.refuse(name, "host_ip %q is not an address", clip(ip))
			continue
		}
		key := netip.AddrPortFrom(addr, uint16(p.Host)).String() + "/" + proto // #nosec G115 -- 1 to 65535, checked above
		if other, ok := c.ports[key]; ok {
			c.refuse(name, "host port %s is also published by %s", key, other)
		}
		c.ports[key] = name
		if !addr.IsLoopback() {
			c.flag(name, "port:"+key)
		}
	}
}

func (c *checker) mounts(name string, svc Service) {
	if len(svc.Volumes) > maxList || len(svc.Tmpfs) > maxList {
		c.refuse(name, "at most %d volumes and %d tmpfs", maxList, maxList)
	}
	targets := map[string]bool{}
	target := func(t string) {
		switch {
		case !cleanAbs(t) || t == "/":
			c.refuse(name, "mount target %q must be an absolute, clean path other than /", clip(t))
		case targets[t]:
			c.refuse(name, "%s is mounted twice", t)
		}
		targets[t] = true
	}
	for _, t := range svc.Tmpfs {
		target(t)
	}
	for _, m := range svc.Volumes {
		target(m.Target)
		switch m.Type {
		case "volume":
			if _, ok := c.s.Volumes[m.Source]; !ok {
				c.refuse(name, "volume %q is not declared", clip(m.Source))
			}
		case "bind":
			c.bind(name, m.Source)
		default:
			c.refuse(name, "a mount's type is volume or bind")
		}
	}
}

// bind applies S12b's bind rules to one source.
func (c *checker) bind(name, src string) {
	if !cleanAbs(src) {
		c.refuse(name, "bind source %q must be an absolute, clean path", clip(src))
		return
	}
	if why := neverBind(src); why != "" {
		c.refuse(name, "a bind of %s is never allowed (%s)", src, why)
		return
	}
	if dockerSocket(src) {
		c.flag(name, "docker-socket")
		return
	}
	if within(src, "/dev") && deviceKind(src) == deviceNever {
		c.refuse(name, "a bind of %s is never allowed (a raw disk or memory device)", src)
		return
	}
	if hostTree(src) != "" {
		c.flag(name, "bind:"+src)
		return
	}
	for _, r := range c.s.BindRoots {
		if within(src, r) {
			return
		}
	}
	c.refuse(name, "bind source %s is not under an approved root", src)
}

func (c *checker) health(name string, h *Healthcheck) {
	if h == nil {
		return
	}
	if h.Disable {
		if len(h.Test) > 0 {
			c.refuse(name, "a disabled healthcheck has no test")
		}
		return
	}
	if len(h.Test) < 1 || len(h.Test) > maxList {
		c.refuse(name, "healthcheck test is a list of 1 to %d", maxList)
		return
	}
	switch h.Test[0] {
	case "CMD":
		if len(h.Test) < 2 {
			c.refuse(name, "healthcheck CMD needs a command")
		}
	case "CMD-SHELL":
		if len(h.Test) != 2 {
			c.refuse(name, "healthcheck CMD-SHELL takes one command line")
		}
	case "NONE":
		if len(h.Test) != 1 {
			c.refuse(name, "healthcheck NONE takes nothing")
		}
	default:
		c.refuse(name, "healthcheck test starts with CMD, CMD-SHELL or NONE")
	}
	c.args(name, "healthcheck", h.Test)
	for _, v := range []int{h.IntervalS, h.TimeoutS, h.StartPeriodS} {
		if v < 0 || v > 3600 {
			c.refuse(name, "healthcheck times are 0 to 3600 s")
		}
	}
	if h.Retries < 0 || h.Retries > 100 {
		c.refuse(name, "healthcheck retries are 0 to 100")
	}
}

func (c *checker) networking(name string, svc Service) {
	if len(svc.Networks) > maxList {
		c.refuse(name, "at most %d networks", maxList)
	}
	for _, n := range svc.Networks {
		if _, ok := c.s.Networks[n]; !ok && n != "default" {
			c.refuse(name, "network %q is not declared", clip(n))
		}
	}
	seen := map[string]bool{}
	for _, n := range svc.Networks {
		if seen[n] {
			c.refuse(name, "network %q is listed twice", clip(n))
		}
		seen[n] = true
	}
	mode := svc.NetworkMode
	switch {
	case mode == "" || mode == "none":
	case mode == "host":
		c.flag(name, "network:host")
	case strings.HasPrefix(mode, "service:"):
		other := strings.TrimPrefix(mode, "service:")
		if _, ok := c.s.Services[other]; !ok || other == name {
			c.refuse(name, "network_mode names no other service of the stack")
		}
	default:
		c.refuse(name, "network_mode must be none, host or service:<name>")
	}
	if mode != "" && len(svc.Networks) > 0 {
		c.refuse(name, "network_mode and networks cannot both be set")
	}
	if mode != "" && len(svc.Ports) > 0 {
		c.refuse(name, "ports cannot be published with a network_mode")
	}
	for _, ns := range []struct{ key, v string }{{"pid", svc.PID}, {"userns", svc.UsernsMode}} {
		switch ns.v {
		case "":
		case "host":
			c.flag(name, ns.key+":host")
		default:
			c.refuse(name, "%s mode must be host or unset", ns.key)
		}
	}
	switch svc.IPC {
	case "", "private", "shareable":
	case "host":
		c.flag(name, "ipc:host")
	default:
		c.refuse(name, "ipc must be private, shareable or host")
	}
}

// neverCaps cannot be added even with an opt-in.
var neverCaps = map[string]bool{"ALL": true, "SYS_ADMIN": true, "SYS_MODULE": true}

func (c *checker) capabilities(name string, svc Service) {
	if len(svc.CapAdd) > maxList || len(svc.CapDrop) > maxList {
		c.refuse(name, "at most %d capabilities", maxList)
	}
	for _, cp := range svc.CapAdd {
		cp = strings.TrimPrefix(cp, "CAP_")
		switch {
		case !capName.MatchString(cp):
			c.refuse(name, "cap_add: %q is not a capability", clip(cp))
		case neverCaps[cp]:
			c.refuse(name, "cap_add %s is never allowed", cp)
		default:
			c.flag(name, "cap:"+cp)
		}
	}
	for _, cp := range svc.CapDrop {
		if cp = strings.TrimPrefix(cp, "CAP_"); !capName.MatchString(cp) {
			c.refuse(name, "cap_drop: %q is not a capability", clip(cp))
		}
	}
}

func (c *checker) deviceList(name string, devices []string) {
	if len(devices) > maxList {
		c.refuse(name, "at most %d devices", maxList)
	}
	for _, d := range devices {
		host, inner, _ := strings.Cut(d, ":")
		if inner != "" && (!cleanAbs(inner) || strings.Contains(inner, ":")) {
			c.refuse(name, "device %q: the container path must be absolute and clean", clip(d))
			continue
		}
		switch deviceKind(host) {
		case deviceNever:
			c.refuse(name, "device %s is never allowed", clip(host))
		case deviceListed:
			c.flag(name, "device:"+host)
		default:
			c.refuse(name, "device %q is not one Hearth can pass through", clip(host))
		}
	}
}

func (c *checker) sysctls(name string, s map[string]string) {
	if len(s) > maxList {
		c.refuse(name, "at most %d sysctls", maxList)
	}
	for k, v := range s {
		if !sysctlName.MatchString(k) || !sysctlVal.MatchString(v) {
			c.refuse(name, "sysctl %q is not a namespaced sysctl Hearth can set", clip(k))
			continue
		}
		c.flag(name, "sysctl:"+k)
	}
}

// dependencies: each names another service, with no cycle.
func (c *checker) dependencies(names []string) {
	for _, n := range names {
		for _, d := range c.s.Services[n].DependsOn {
			if _, ok := c.s.Services[d]; !ok || d == n {
				c.refuse(n, "depends_on %q names no other service", clip(d))
			}
		}
	}
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(n string) bool
	visit = func(n string) bool {
		switch state[n] {
		case 1:
			return false
		case 2:
			return true
		}
		state[n] = 1
		for _, d := range c.s.Services[n].DependsOn {
			if _, ok := c.s.Services[d]; ok && d != n && !visit(d) {
				return false
			}
		}
		state[n] = 2
		return true
	}
	for _, n := range names {
		if !visit(n) {
			c.refuse("spec", "depends_on has a cycle")
			return
		}
	}
}

// composeSetting: Compose reads COMPOSE_* and DOCKER_* from its env file, so
// a secret by such a name could change which file it runs or which daemon it
// talks to.
func composeSetting(k string) bool {
	u := strings.ToUpper(k)
	return strings.HasPrefix(u, "COMPOSE_") || strings.HasPrefix(u, "DOCKER_") || u == "BUILDKIT_PROGRESS"
}

// ValidName reports whether s is a stack name.
func ValidName(s string) bool { return stackName.MatchString(s) }

// cleanAbs: absolute, already clean, bounded, printable.
func cleanAbs(p string) bool {
	return len(p) <= maxPathLen && strings.HasPrefix(p, "/") && path.Clean(p) == p && printable(p)
}

func printable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '�' })
}

// within reports whether p is root or lies under it.
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// neverBind says why a bind source is never allowed, or "".
func neverBind(p string) string {
	switch {
	case p == "/":
		return "the whole host"
	case within(p, "/etc"):
		return "host configuration"
	case within(p, "/var/lib/hearth"):
		return "Hearth's own state"
	case within(p, "/var/lib/docker"), within(p, "/var/lib/containerd"):
		return "Docker's own state"
	case within(p, "/boot"):
		return "the boot files"
	case magicProc.MatchString(p):
		return "a /proc link to another root"
	}
	return ""
}

// magicProc: /proc/<pid>/root and its siblings are other roots by another
// name; so are /proc/self and /proc/thread-self themselves.
var magicProc = regexp.MustCompile(`^/proc/(?:self|thread-self|[0-9]+)(?:$|/(?:root|cwd|fd|map_files|exe)(?:$|/))|^/proc/(?:self|thread-self)/`)

// hostTree names the host tree a bind lies in that S12b allows only with an
// opt-in, or "".
func hostTree(p string) string {
	for _, t := range []string{"/root", "/proc", "/sys", "/dev", "/run", "/var/run"} {
		if within(p, t) {
			return t
		}
	}
	return ""
}

func dockerSocket(p string) bool {
	return p == "/var/run/docker.sock" || p == "/run/docker.sock"
}

// reservedLabel: labels Compose, Docker and Hearth set themselves.
func reservedLabel(k string) bool {
	for _, p := range []string{"com.docker.", "io.docker.", "org.dockerproject.", "org.opencontainers.", "org.hearth."} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

type deviceClass int

const (
	deviceUnknown deviceClass = iota
	deviceListed
	deviceNever
)

var (
	// Raw disks and memory: a container with them owns the host.
	neverDevice = regexp.MustCompile(`^/dev/(?:mem|kmem|port|(?:sd|hd|vd|xvd)[a-z]+[0-9]*|nvme[0-9]+(?:n[0-9]+(?:p[0-9]+)?)?|mmcblk[0-9]+(?:p[0-9]+)?|loop[0-9]+|loop-control|dm-[0-9]+|md[0-9]+|sr[0-9]+|mapper(?:/.*)?|disk(?:/.*)?|block(?:/.*)?|nbd[0-9]+|zram[0-9]+)$`)
	// GPUs, tun, sound, video, serial and USB adapters, fuse and kvm.
	listedDevice = regexp.MustCompile(`^/dev/(?:dri(?:/(?:card|renderD)[0-9]{1,3})?|nvidia[0-9]{1,2}|nvidiactl|nvidia-uvm|nvidia-uvm-tools|nvidia-modeset|kfd|net/tun|snd|video[0-9]{1,2}|ttyUSB[0-9]{1,2}|ttyACM[0-9]{1,2}|bus/usb(?:/[0-9]{3}(?:/[0-9]{3})?)?|fuse|kvm|hidraw[0-9]{1,2}|ttyS[0-9]{1,2})$`)
)

func deviceKind(p string) deviceClass {
	if !cleanAbs(p) {
		return deviceUnknown
	}
	switch {
	case neverDevice.MatchString(p):
		return deviceNever
	case listedDevice.MatchString(p):
		return deviceListed
	}
	return deviceUnknown
}

// FlagKinds lists the flag forms, for the UI's opt-in list and the docs.
var FlagKinds = []string{
	"cap:<NAME>", "device:<path>", "network:host", "pid:host", "ipc:host", "userns:host",
	"sysctl:<name>", "docker-socket", "bind:<path under /root, /proc, /sys, /dev, /run>", "port:<address>:<port>/<proto>",
}
