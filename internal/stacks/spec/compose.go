package spec

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// LabelStack marks every container of a stack with its name.
const LabelStack = "org.hearth.stack"

// LabelSecrets lists, comma-separated, the secret keys a container
// receives from secrets.env.
const LabelSecrets = "org.hearth.secrets" // #nosec G101 -- a label name; it lists key names, never values

// DefaultRestart is the restart policy of a service whose spec names none.
const DefaultRestart = "unless-stopped"

// Compose generates the canonical compose file (ROADMAP 9.2) from a spec
// alone: only what Check accepted reaches it, as JSON with sorted keys, so
// the same spec always gives the same bytes. Every "$" in a value becomes
// "$$", so Compose interpolates nothing, except the ${NAME} references to
// secrets.env written here for the keys a service names.
func Compose(s Spec) ([]byte, error) {
	if r := Check(s); !r.OK() {
		return nil, errors.New("the spec is refused: " + strings.Join(r.Refused, "; "))
	}
	services := map[string]any{}
	for name, svc := range s.Services {
		services[name] = service(s.Name, svc)
	}
	out := map[string]any{"name": s.Name, "services": services}
	if len(s.Volumes) > 0 {
		vols := map[string]any{}
		for k, v := range s.Volumes {
			m := map[string]any{}
			if v.Name != "" {
				m["name"] = v.Name
			}
			if v.External {
				m["external"] = true
			}
			vols[k] = m
		}
		out["volumes"] = vols
	}
	if len(s.Networks) > 0 {
		nets := map[string]any{}
		for k, n := range s.Networks {
			m := map[string]any{}
			if n.Name != "" {
				m["name"] = n.Name
			}
			if n.External {
				m["external"] = true
			}
			if n.Internal {
				m["internal"] = true
			}
			nets[k] = m
		}
		out["networks"] = nets
	}
	return json.Marshal(out)
}

// lit escapes a value for Compose: "$$" is a literal "$".
func lit(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func lits(l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = lit(s)
	}
	return out
}

func litMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = lit(v)
	}
	return out
}

func service(stack string, svc Service) map[string]any {
	m := map[string]any{"image": svc.Image}
	set := func(k, v string) {
		if v != "" {
			m[k] = lit(v)
		}
	}
	set("pull_policy", svc.PullPolicy)
	set("container_name", svc.ContainerName)
	// An app comes back after a reboot unless its spec says otherwise.
	restart := svc.Restart
	if restart == "" {
		restart = DefaultRestart
	}
	set("restart", restart)
	set("user", svc.User)
	set("working_dir", svc.WorkingDir)
	set("hostname", svc.Hostname)
	set("network_mode", svc.NetworkMode)
	set("pid", svc.PID)
	set("ipc", svc.IPC)
	set("userns_mode", svc.UsernsMode)
	set("cpus", svc.CPUs)
	set("stop_signal", svc.StopSignal)
	if len(svc.Command) > 0 {
		m["command"] = lits(svc.Command)
	}
	if len(svc.Entrypoint) > 0 {
		m["entrypoint"] = lits(svc.Entrypoint)
	}
	if env := environment(svc); len(env) > 0 {
		m["environment"] = env
	}
	if len(svc.Ports) > 0 {
		ports := make([]any, len(svc.Ports))
		for i, p := range svc.Ports {
			ip, proto := p.HostIP, p.Protocol
			if ip == "" {
				ip = "127.0.0.1"
			}
			if proto == "" {
				proto = "tcp"
			}
			ports[i] = map[string]any{"target": p.Container, "published": strconv.Itoa(p.Host), "host_ip": ip, "protocol": proto}
		}
		m["ports"] = ports
	}
	if len(svc.Volumes) > 0 {
		vols := make([]any, len(svc.Volumes))
		for i, v := range svc.Volumes {
			mv := map[string]any{"type": v.Type, "source": lit(v.Source), "target": lit(v.Target)}
			if v.ReadOnly {
				mv["read_only"] = true
			}
			if v.Type == "bind" {
				// A missing source is an error, never a folder made by root.
				mv["bind"] = map[string]any{"create_host_path": false}
			}
			vols[i] = mv
		}
		m["volumes"] = vols
	}
	if len(svc.Tmpfs) > 0 {
		m["tmpfs"] = lits(svc.Tmpfs)
	}
	if len(svc.DependsOn) > 0 {
		m["depends_on"] = svc.DependsOn
	}
	if h := svc.Healthcheck; h != nil {
		hc := map[string]any{}
		if h.Disable {
			hc["disable"] = true
		} else {
			hc["test"] = lits(h.Test)
			for k, v := range map[string]int{"interval": h.IntervalS, "timeout": h.TimeoutS, "start_period": h.StartPeriodS} {
				if v > 0 {
					hc[k] = strconv.Itoa(v) + "s"
				}
			}
			if h.Retries > 0 {
				hc["retries"] = h.Retries
			}
		}
		m["healthcheck"] = hc
	}
	labels := litMap(svc.Labels)
	labels[LabelStack] = stack
	if len(svc.Secrets) > 0 {
		// Names only: Hearth's inspect masks these keys, whatever they are called.
		keys := slices.Clone(svc.Secrets)
		slices.Sort(keys)
		labels[LabelSecrets] = strings.Join(keys, ",")
	}
	m["labels"] = labels
	if len(svc.Networks) > 0 {
		m["networks"] = svc.Networks
	}
	if len(svc.CapAdd) > 0 {
		m["cap_add"] = noCapPrefix(svc.CapAdd)
	}
	if len(svc.CapDrop) > 0 {
		m["cap_drop"] = noCapPrefix(svc.CapDrop)
	}
	if len(svc.Devices) > 0 {
		devs := make([]string, len(svc.Devices))
		for i, d := range svc.Devices {
			if !strings.Contains(d, ":") {
				d += ":" + d
			}
			devs[i] = lit(d)
		}
		m["devices"] = devs
	}
	if len(svc.Sysctls) > 0 {
		m["sysctls"] = litMap(svc.Sysctls)
	}
	if len(svc.SecurityOpt) > 0 {
		m["security_opt"] = []string{"no-new-privileges:true"}
	}
	if len(svc.ExtraHosts) > 0 {
		m["extra_hosts"] = litMap(svc.ExtraHosts)
	}
	if svc.MemoryMB > 0 {
		m["mem_limit"] = int64(svc.MemoryMB) << 20
	}
	if svc.ShmSizeMB > 0 {
		m["shm_size"] = int64(svc.ShmSizeMB) << 20
	}
	if svc.PidsLimit > 0 {
		m["pids_limit"] = svc.PidsLimit
	}
	if svc.ReadOnly {
		m["read_only"] = true
	}
	if svc.Init {
		m["init"] = true
	}
	if svc.StopGraceS > 0 {
		m["stop_grace_period"] = strconv.Itoa(svc.StopGraceS) + "s"
	}
	return m
}

// environment: plain values escaped, secrets as references the env file
// resolves. A service gets only the secret keys it names.
func environment(svc Service) map[string]string {
	env := litMap(svc.Environment)
	for _, k := range svc.Secrets {
		env[k] = "${" + k + "}"
	}
	return env
}

func noCapPrefix(l []string) []string {
	out := make([]string, len(l))
	for i, c := range l {
		out[i] = strings.TrimPrefix(c, "CAP_")
	}
	return out
}
