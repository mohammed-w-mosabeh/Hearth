package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Compose update (ROADMAP 7.6, SECURITY S16h). hearthd renders a project with
// `docker compose config` as the user, since a file read as root could quote
// root's files into a container. Here that rendering is untrusted input. It is
// checked against closed lists, because Compose grows keys and some of them
// run things on the host (provider: starts a host binary) or equal a bind
// mount (a local volume's driver_opts): what is not listed is refused. Only
// the checked value, marshalled again, is written, so a duplicate key cannot
// read one way here and another way to Compose. Compose runs on that file in
// hearth-compose.service and never sees the project's own files.
const (
	// ActionCompose is the polkit action: it starts containers, so
	// auth_admin with no keep.
	ActionCompose = "org.hearth.docker.compose"
	// ComposeUnit runs Compose, in a sandbox and memory limit of its own.
	ComposeUnit = "hearth-compose.service"
	// ComposeFile and ComposeArgs are what the unit reads.
	ComposeFile = "/var/lib/hearth/compose.json"
	ComposeArgs = "/var/lib/hearth/compose.args"
	// ComposeBinary tells whether Docker's command line is there.
	ComposeBinary = "/usr/bin/docker"
	// ComposeTimeout bounds compose.update: the unit's own 30 min limit, and
	// a polkit dialog.
	ComposeTimeout = 32 * time.Minute

	// MaxComposeConfig is the largest rendering accepted; a request must
	// fit in MaxLine with everything else, escaped.
	MaxComposeConfig = 40 << 10
	maxComposeFiles  = 4

	// Labels Compose cannot be asked to keep right: it records the file it
	// was given, here ComposeFile, so the project's own ones go in these.
	LabelComposeFiles = "org.hearth.compose.files"
	LabelComposeDir   = "org.hearth.compose.dir"
)

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

var (
	composeTop = keySet("name", "services", "networks", "volumes")
	// Everything a service may have. Left out on purpose, among others:
	// privileged, cap_add, pid, ipc, uts, userns_mode, cgroup, cgroup_parent,
	// devices, device_cgroup_rules, security_opt, sysctls, runtime, build,
	// provider, develop, post_start, pre_stop, volumes_from, secrets, configs,
	// credential_spec, group_add, external_links, links.
	composeService = keySet("image", "command", "entrypoint", "environment", "labels", "ports", "expose",
		"networks", "network_mode", "volumes", "tmpfs", "restart", "depends_on", "healthcheck", "hostname",
		"domainname", "container_name", "user", "working_dir", "mem_limit", "mem_reservation", "memswap_limit",
		"cpus", "cpu_shares", "pids_limit", "stop_grace_period", "stop_signal", "tty", "stdin_open", "read_only",
		"init", "logging", "shm_size", "ulimits", "dns", "dns_search", "extra_hosts", "pull_policy", "platform",
		"cap_drop", "deploy", "scale", "mac_address")
	composeNetwork = keySet("name", "external", "driver", "internal", "attachable", "enable_ipv6", "labels", "ipam")
	composeVolume  = keySet("name", "external", "driver", "labels")
	composeMount   = keySet("type", "source", "target", "read_only", "bind", "volume", "tmpfs", "consistency")
	composeBind    = keySet("create_host_path", "selinux")
	composeLogging = keySet("json-file", "local", "journald", "none")
	composeDeploy  = keySet("replicas", "resources", "restart_policy")
	composeLimit   = keySet("cpus", "memory", "pids")
	composePull    = keySet("always", "never", "missing", "if_not_present", "daily", "weekly")
)

// bridgeDrivers are the network drivers that stay on Docker's own bridges.
var bridgeDrivers = keySet("", "bridge", "default")

// ComposeCheck is the result of CheckCompose.
type ComposeCheck struct {
	// Services maps each service to its image.
	Services map[string]string
	// Refused says what is not allowed, as "service: what"; empty means
	// Config may be used.
	Refused []string
	// Config is the checked value, without x- extensions.
	Config map[string]any
}

// CheckCompose checks a rendering of project. Bad input is an error; a
// well-formed config that cannot be used lists why in Refused. hearthd calls
// it for the preview and the broker again on what it is sent.
func CheckCompose(raw []byte, project string) (ComposeCheck, error) {
	c := ComposeCheck{Services: map[string]string{}}
	if len(raw) > MaxComposeConfig {
		return c, fmt.Errorf("the rendered project is over %d KiB", MaxComposeConfig>>10)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&c.Config); err != nil || dec.More() || c.Config == nil {
		return ComposeCheck{Services: c.Services}, errors.New("the rendered project is not one JSON object")
	}
	refuse := func(who, what string) { c.Refused = append(c.Refused, who+": "+what) }
	for k := range c.Config {
		if strings.HasPrefix(k, "x-") {
			delete(c.Config, k)
		} else if !composeTop[k] {
			refuse("project", k)
		}
	}
	if name, _ := c.Config["name"].(string); name != project {
		refuse("project", "another project's name")
	}
	services, ok := c.Config["services"].(map[string]any)
	if !ok || len(services) == 0 {
		refuse("project", "no services")
	}
	for name, v := range services {
		svc, ok := v.(map[string]any)
		if !ok {
			refuse(name, "not a service")
			continue
		}
		c.Services[name], _ = svc["image"].(string)
		checkService(name, svc, services, refuse)
	}
	if nets, ok := objectOrNil(c.Config["networks"]); ok {
		for name, v := range nets {
			checkTopLevel("network "+name, v, composeNetwork, refuse)
			if n, ok := v.(map[string]any); ok {
				if d, _ := n["driver"].(string); !bridgeDrivers[d] {
					refuse("network "+name, "driver "+d)
				}
			}
		}
	} else {
		refuse("project", "networks")
	}
	if vols, ok := objectOrNil(c.Config["volumes"]); ok {
		for name, v := range vols {
			checkTopLevel("volume "+name, v, composeVolume, refuse)
			if n, ok := v.(map[string]any); ok {
				if d, _ := n["driver"].(string); d != "" && d != "local" {
					refuse("volume "+name, "driver "+d)
				}
			}
		}
	} else {
		refuse("project", "volumes")
	}
	sort.Strings(c.Refused)
	c.Refused = slices.Compact(c.Refused)
	return c, nil
}

// objectOrNil reads an optional object.
func objectOrNil(v any) (map[string]any, bool) {
	if v == nil {
		return nil, true
	}
	m, ok := v.(map[string]any)
	return m, ok
}

func checkTopLevel(who string, v any, allowed map[string]bool, refuse func(who, what string)) {
	m, ok := objectOrNil(v)
	if !ok {
		refuse(who, "not an object")
		return
	}
	for k := range m {
		if !allowed[k] && !strings.HasPrefix(k, "x-") {
			refuse(who, k)
		}
	}
}

func checkService(name string, svc, services map[string]any, refuse func(who, what string)) {
	for k := range svc {
		if strings.HasPrefix(k, "x-") {
			delete(svc, k)
		} else if !composeService[k] {
			refuse(name, k)
		}
	}
	image, _ := svc["image"].(string)
	if _, _, _, ok := ParseImageRef(image); !ok {
		refuse(name, "no usable image")
	}
	if v, ok := svc["pull_policy"]; ok {
		if s, _ := v.(string); !composePull[s] {
			refuse(name, "pull_policy")
		}
	}
	if v, ok := svc["network_mode"]; ok {
		s, _ := v.(string)
		other, joined := strings.CutPrefix(s, "service:")
		_, known := services[other]
		if s != "bridge" && s != "none" && (!joined || !known) {
			refuse(name, "network_mode "+s)
		}
	}
	if v, ok := svc["logging"]; ok {
		l, _ := v.(map[string]any)
		d, _ := l["driver"].(string)
		if d != "" && !composeLogging[d] {
			refuse(name, "logging driver "+d)
		}
	}
	checkDeploy(name, svc["deploy"], refuse)
	if v, ok := svc["volumes"]; ok {
		list, ok := v.([]any)
		if !ok {
			refuse(name, "volumes")
			return
		}
		for _, m := range list {
			checkMount(name, m, refuse)
		}
	}
}

func checkDeploy(name string, v any, refuse func(who, what string)) {
	d, ok := objectOrNil(v)
	if !ok {
		refuse(name, "deploy")
		return
	}
	for k := range d {
		if !composeDeploy[k] {
			refuse(name, "deploy."+k)
		}
	}
	res, ok := objectOrNil(d["resources"])
	if !ok {
		refuse(name, "deploy.resources")
		return
	}
	for section, sv := range res {
		lim, ok := sv.(map[string]any)
		if !ok || (section != "limits" && section != "reservations") {
			refuse(name, "deploy.resources."+section)
			continue
		}
		for k := range lim {
			if !composeLimit[k] {
				refuse(name, "deploy.resources."+section+"."+k)
			}
		}
	}
}

func checkMount(name string, v any, refuse func(who, what string)) {
	m, ok := v.(map[string]any)
	if !ok {
		refuse(name, "a volume that is not in long form")
		return
	}
	for k := range m {
		if !composeMount[k] {
			refuse(name, "volume "+k)
		}
	}
	kind, _ := m["type"].(string)
	source, _ := m["source"].(string)
	switch kind {
	case "volume", "tmpfs":
	case "bind":
		if !filepath.IsAbs(source) || filepath.Clean(source) != source {
			refuse(name, "bind mount "+source)
		} else if riskyMount(source) {
			refuse(name, RiskHostMount+":"+source)
		}
		if b, ok := objectOrNil(m["bind"]); !ok {
			refuse(name, "bind options")
		} else {
			for k := range b {
				if !composeBind[k] {
					refuse(name, "bind "+k)
				}
			}
		}
	default:
		refuse(name, "volume type "+kind)
	}
}

// ComposeUpdateParams is compose.update's params.
type ComposeUpdateParams struct {
	Project string          `json:"project"`
	Dir     string          `json:"dir"`
	Files   []string        `json:"files"`
	Config  json.RawMessage `json:"config"`
}

var composeProjectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

// composePath is an absolute clean path that survives a comma-joined label.
func composePath(s string) bool {
	if !filepath.IsAbs(s) || filepath.Clean(s) != s || len(s) > 1024 || strings.Contains(s, ",") {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// CheckComposeUpdate validates params; it is the whole gate before polkit.
func CheckComposeUpdate(p ComposeUpdateParams) (ComposeCheck, error) {
	if !composeProjectRe.MatchString(p.Project) {
		return ComposeCheck{}, errors.New("not a Compose project name")
	}
	if !composePath(p.Dir) || len(p.Files) == 0 || len(p.Files) > maxComposeFiles {
		return ComposeCheck{}, errors.New("bad project paths")
	}
	for _, f := range p.Files {
		if !composePath(f) {
			return ComposeCheck{}, errors.New("bad project paths")
		}
	}
	c, err := CheckCompose(p.Config, p.Project)
	if err != nil {
		return c, err
	}
	if len(c.Refused) > 0 {
		return c, errors.New("refused: " + strings.Join(c.Refused, "; "))
	}
	return c, nil
}

// ComposeConfig configures ComposeMethods; zero paths take the constants.
type ComposeConfig struct {
	Bus  func(ctx context.Context) (SystemBus, error)
	File string
	Args string
	Poll time.Duration
	// Services lists the services a project has containers for.
	Services func(ctx context.Context, project string) ([]string, error)
}

func (c ComposeConfig) write(path string, b []byte) error {
	_ = os.Remove(path)
	// O_EXCL: a link in the way is refused, not followed.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a fixed path in the 0700 state directory
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	return errors.Join(werr, f.Close())
}

// ComposeMethods is compose.update. An existing project only: a service it
// has no container for is refused, so this never brings up something new.
func ComposeMethods(c ComposeConfig) []Method {
	if c.File == "" {
		c.File = ComposeFile
	}
	if c.Args == "" {
		c.Args = ComposeArgs
	}
	var mu sync.Mutex // one at a time: there is one file
	parse := func(p json.RawMessage) (ComposeUpdateParams, ComposeCheck, error) {
		var v ComposeUpdateParams
		if err := decodeParams(p, &v); err != nil {
			return v, ComposeCheck{}, err
		}
		chk, err := CheckComposeUpdate(v)
		if err != nil {
			return v, chk, badParam("%s", err.Error())
		}
		return v, chk, nil
	}
	return []Method{{
		Name: "compose.update", Privileged: true, Action: ActionCompose, Timeout: ComposeTimeout,
		Check: func(_ context.Context, p json.RawMessage) error { _, _, err := parse(p); return err },
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			v, chk, err := parse(p)
			if err != nil {
				return nil, err
			}
			have, err := c.Services(ctx, v.Project)
			if err != nil {
				return nil, Errorf(CodeFailed, "compose: could not list the project")
			}
			if len(have) == 0 {
				return nil, Errorf(CodeRefused, "no such project")
			}
			for name := range chk.Services {
				if !slices.Contains(have, name) {
					return nil, Errorf(CodeRefused, "service %s is new: bring it up yourself once", name)
				}
			}
			addLabels(chk.Config, v)
			out, err := json.Marshal(chk.Config)
			if err != nil {
				return nil, Errorf(CodeFailed, "compose: could not prepare the update")
			}
			mu.Lock()
			defer mu.Unlock()
			defer func() { _ = os.Remove(c.File); _ = os.Remove(c.Args) }()
			if err := errors.Join(c.write(c.File, out), c.write(c.Args, []byte("COMPOSE_PROJECT="+v.Project+"\n"))); err != nil {
				return nil, Errorf(CodeFailed, "compose: could not prepare the update")
			}
			if err := runOneshot(ctx, c.Bus, c.Poll, "compose", ComposeUnit); err != nil {
				return nil, err
			}
			return map[string]bool{"done": true}, nil
		},
	}}
}

func addLabels(cfg map[string]any, v ComposeUpdateParams) {
	services, _ := cfg["services"].(map[string]any)
	for _, s := range services {
		svc, ok := s.(map[string]any)
		if !ok {
			continue
		}
		labels, ok := svc["labels"].(map[string]any)
		if !ok {
			labels = map[string]any{}
			svc["labels"] = labels
		}
		labels[LabelComposeFiles] = strings.Join(v.Files, ",")
		labels[LabelComposeDir] = v.Dir
	}
}
