package spec

import (
	"sort"
	"strings"
)

// Bind is one bind mount of a checked spec.
type Bind struct {
	Service string `json:"service"`
	Source  string `json:"source"`
	Target  string `json:"target"`
}

// Binds lists a spec's bind mounts, in service then target order.
func Binds(s Spec) []Bind {
	var out []Bind
	for name, svc := range s.Services {
		for _, m := range svc.Volumes {
			if m.Type == "bind" {
				out = append(out, Bind{Service: name, Source: m.Source, Target: m.Target})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Target < out[j].Target
	})
	return out
}

// CheckResolved applies S12b's bind rules to the real path of a bind
// source, after its links were resolved (ROADMAP 9.3). real must be what
// the kernel resolved src to; roots maps each of the spec's bind roots to
// its own real path. It returns why the bind is refused, or "".
//
// A source the spec flags (a host tree or the Docker socket) was opted in
// by its exact path, so it may not pass through any link. Any other source
// must still lie, resolved, under a resolved root, and its real path must
// pass every rule its written path passed: a link cannot turn an approved
// folder into /etc, a host tree or the Docker socket.
func CheckResolved(s Spec, roots map[string]string, src, real string) string {
	if !cleanAbs(real) {
		return "it resolves to a path that is not absolute and clean"
	}
	if why := neverBind(real); why != "" {
		return "it resolves to " + real + ", which is never allowed (" + why + ")"
	}
	if within(real, "/dev") && deviceKind(real) == deviceNever {
		return "it resolves to " + real + ", a raw disk or memory device"
	}
	if dockerSocket(src) || hostTree(src) != "" {
		if real != canonicalRun(src) {
			return "it was opted in by its path but is reached through a link, to " + real
		}
		return ""
	}
	if dockerSocket(real) || hostTree(real) != "" {
		return "it resolves to " + real + ", which needs its own opt-in"
	}
	for _, r := range s.BindRoots {
		rr, ok := roots[r]
		if !ok || !cleanAbs(rr) || rr == "/" || neverBind(rr) != "" || hostTree(rr) != "" || dockerSocket(rr) {
			continue
		}
		if within(src, r) && within(real, rr) {
			return ""
		}
	}
	return "it resolves to " + real + ", outside its approved root"
}

// CheckRoot applies the bind_roots rules to the real path of a root.
func CheckRoot(real string) string {
	switch {
	case !cleanAbs(real):
		return "it resolves to a path that is not absolute and clean"
	case real == "/":
		return "it resolves to /"
	case neverBind(real) != "" || hostTree(real) != "" || dockerSocket(real):
		return "it resolves to " + real + ", which cannot be a root"
	}
	return ""
}

// canonicalRun: /var/run is a link to /run on Debian, the one link a
// flagged path may pass through.
func canonicalRun(p string) string {
	if within(p, "/var/run") {
		return "/run" + strings.TrimPrefix(p, "/var/run")
	}
	return p
}
