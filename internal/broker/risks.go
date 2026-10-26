package broker

import (
	"path"
	"strings"
)

// HostConfig is the part of a container's HostConfig that SECURITY S12
// is about.
type HostConfig struct {
	Privileged  bool
	CapAdd      []string
	PidMode     string
	NetworkMode string
	IpcMode     string
	Binds       []string // "src:dst[:opts]"
	Mounts      []struct {
		Type   string
		Source string
	}
}

// Risk reasons.
const (
	RiskPrivileged = "privileged"
	RiskCapAdd     = "cap-add"
	RiskHostPID    = "host-pid"
	RiskHostNet    = "host-network"
	RiskHostIPC    = "host-ipc"
	RiskHostMount  = "host-mount" // followed by ":" and the path
)

// Bind mounts of these host paths (or below them, except "/" itself) are
// risky; the Docker socket anywhere it is found.
var riskyRoots = []string{"/etc", "/root", "/proc", "/sys", "/dev", "/boot"}

// Risks lists why a container with this HostConfig could take over the
// host (SECURITY S12). The broker has no method that creates a
// container; a future one must refuse a config with any risk. Existing
// containers are shown with their risks (Security posture).
func Risks(hc HostConfig) []string {
	var out []string
	if hc.Privileged {
		out = append(out, RiskPrivileged)
	}
	if len(hc.CapAdd) > 0 {
		out = append(out, RiskCapAdd)
	}
	if hc.PidMode == "host" {
		out = append(out, RiskHostPID)
	}
	if hc.NetworkMode == "host" {
		out = append(out, RiskHostNet)
	}
	if hc.IpcMode == "host" {
		out = append(out, RiskHostIPC)
	}
	var srcs []string
	for _, b := range hc.Binds {
		src, _, _ := strings.Cut(b, ":")
		srcs = append(srcs, src)
	}
	for _, m := range hc.Mounts {
		if m.Type == "bind" {
			srcs = append(srcs, m.Source)
		}
	}
	for _, src := range srcs {
		if riskyMount(src) {
			out = append(out, RiskHostMount+":"+src)
		}
	}
	return out
}

func riskyMount(src string) bool {
	if !strings.HasPrefix(src, "/") {
		return false // a named volume
	}
	p := path.Clean(src)
	if p == "/" || path.Base(p) == "docker.sock" {
		return true
	}
	for _, r := range riskyRoots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	// /var/run is /run on Debian.
	return p == "/run" || p == "/var/run"
}
