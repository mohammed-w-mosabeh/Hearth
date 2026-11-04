// Package spec is the app spec of SECURITY S12b (ROADMAP 9.1): the only
// way a stack is described to Hearth. It is a versioned JSON document,
// decoded strictly against closed lists at every level, and checked here
// with no I/O, so the broker and hearthd apply the same rules. The canonical
// compose file (9.2) is generated from a decoded Spec alone.
//
// What has no field cannot be said: privileged, build, env_file, extends,
// include, profiles, provider, lifecycle hooks, volumes_from, cgroup_parent,
// credential_spec, driver_opts, network and logging drivers, configs, x-
// extensions. YAML is not JSON and is refused as such.
package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

const (
	// Version is the only spec version.
	Version = 1
	// MaxSpec bounds a spec in bytes.
	MaxSpec = 64 << 10
	// maxDepth bounds nesting; the deepest legal path is 5.
	maxDepth = 8
)

// Spec is one stack.
type Spec struct {
	Version  int                `json:"version"`
	Name     string             `json:"name"`
	Services map[string]Service `json:"services"`
	Volumes  map[string]Volume  `json:"volumes,omitempty"`
	Networks map[string]Network `json:"networks,omitempty"`
	// BindRoots are the host folders the owner approved for this stack's
	// bind mounts; every ordinary bind source lies under one of them.
	BindRoots []string `json:"bind_roots,omitempty"`
	// OptIn lists the danger flags the owner opted in to, one by one, in
	// the form Check reports them (see flags.go).
	OptIn []string `json:"opt_in,omitempty"`
}

// Service is one container.
type Service struct {
	Image string `json:"image"`
	// PullPolicy: "" (missing), "always", "missing" or "never"; never is a
	// local image, which Hearth does not update.
	PullPolicy    string            `json:"pull_policy,omitempty"`
	ContainerName string            `json:"container_name,omitempty"`
	Command       []string          `json:"command,omitempty"`
	Entrypoint    []string          `json:"entrypoint,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
	// Secrets names keys of the stack's secrets.env this service receives.
	Secrets     []string          `json:"secrets,omitempty"`
	Ports       []Port            `json:"ports,omitempty"`
	Volumes     []Mount           `json:"volumes,omitempty"`
	Tmpfs       []string          `json:"tmpfs,omitempty"`
	DependsOn   []string          `json:"depends_on,omitempty"`
	Restart     string            `json:"restart,omitempty"`
	Healthcheck *Healthcheck      `json:"healthcheck,omitempty"`
	User        string            `json:"user,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Hostname    string            `json:"hostname,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Networks    []string          `json:"networks,omitempty"`
	// NetworkMode: "" (the stack's networks), "none", "host" (a flag) or
	// "service:<name>".
	NetworkMode string            `json:"network_mode,omitempty"`
	PID         string            `json:"pid,omitempty"`         // "" or "host" (a flag)
	IPC         string            `json:"ipc,omitempty"`         // "", "private", "shareable" or "host" (a flag)
	UsernsMode  string            `json:"userns_mode,omitempty"` // "" or "host" (a flag)
	CapAdd      []string          `json:"cap_add,omitempty"`
	CapDrop     []string          `json:"cap_drop,omitempty"`
	Devices     []string          `json:"devices,omitempty"`
	Sysctls     map[string]string `json:"sysctls,omitempty"`
	SecurityOpt []string          `json:"security_opt,omitempty"`
	ExtraHosts  map[string]string `json:"extra_hosts,omitempty"`
	MemoryMB    int               `json:"memory_mb,omitempty"`
	CPUs        string            `json:"cpus,omitempty"`
	PidsLimit   int               `json:"pids_limit,omitempty"`
	ShmSizeMB   int               `json:"shm_size_mb,omitempty"`
	ReadOnly    bool              `json:"read_only,omitempty"`
	Init        bool              `json:"init,omitempty"`
	StopGraceS  int               `json:"stop_grace_period_s,omitempty"`
	StopSignal  string            `json:"stop_signal,omitempty"`
}

// Port publishes a container port. HostIP defaults to 127.0.0.1; any other
// address is exposure, a flag.
type Port struct {
	HostIP    string `json:"host_ip,omitempty"`
	Host      int    `json:"host"`
	Container int    `json:"container"`
	Protocol  string `json:"protocol,omitempty"` // "" (tcp), "tcp" or "udp"
}

// Mount is a named volume or a bind mount, in long form only.
type Mount struct {
	Type     string `json:"type"` // "volume" or "bind"
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// Healthcheck is the container's own check. CMD-SHELL runs a shell inside
// the container, never on the host.
type Healthcheck struct {
	Test         []string `json:"test,omitempty"`
	IntervalS    int      `json:"interval_s,omitempty"`
	TimeoutS     int      `json:"timeout_s,omitempty"`
	StartPeriodS int      `json:"start_period_s,omitempty"`
	Retries      int      `json:"retries,omitempty"`
	Disable      bool     `json:"disable,omitempty"`
}

// Volume is a named volume, always with the local driver. Name is the
// Docker volume's own name when it is not <stack>_<key>, as after an import.
type Volume struct {
	Name     string `json:"name,omitempty"`
	External bool   `json:"external,omitempty"`
}

// Network is a bridge network of the stack.
type Network struct {
	Name     string `json:"name,omitempty"`
	External bool   `json:"external,omitempty"`
	Internal bool   `json:"internal,omitempty"`
}

// Decode reads a spec strictly: one JSON object of at most MaxSpec bytes,
// valid UTF-8, no duplicate key and no null at any level, nesting bounded,
// and every key one of the fields at its place, in exact case. Decode checks
// structure only; Check applies the rules.
func Decode(raw []byte) (Spec, error) {
	var s Spec
	if len(raw) > MaxSpec {
		return s, fmt.Errorf("the spec is over %d KiB", MaxSpec>>10)
	}
	if !utf8.Valid(raw) {
		return s, errors.New("the spec is not valid UTF-8")
	}
	if err := scan(raw); err != nil {
		return s, err
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return s, errors.New("the spec is not JSON")
	}
	if err := keys(generic, reflect.TypeFor[Spec](), "spec"); err != nil {
		return s, err
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("the spec does not fit its format: %s", typeError(err))
	}
	return s, nil
}

// typeError keeps a decoding error short and free of input text.
func typeError(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return "wrong type at " + te.Field
	}
	return "bad value"
}

// scan walks the tokens: one value, no duplicate key, no null, bounded depth.
func scan(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	type frame struct {
		object bool
		seen   map[string]bool
		key    bool // the next token in an object is a key
	}
	var stack []*frame
	values := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("the spec is not JSON")
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.object && top.key {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				continue
			}
			k := tok.(string) // the decoder guarantees a key here
			if top.seen[k] {
				return fmt.Errorf("the key %q appears twice", clip(k))
			}
			top.seen[k] = true
			top.key = false
			continue
		}
		if len(stack) == 0 {
			values++
			if values > 1 {
				return errors.New("the spec holds more than one value")
			}
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				if top != nil && top.object {
					top.key = true
				}
				if len(stack) >= maxDepth {
					return errors.New("the spec is nested too deeply")
				}
				stack = append(stack, &frame{object: t == '{', seen: map[string]bool{}, key: t == '{'})
			case ']':
				stack = stack[:len(stack)-1]
			}
		case nil:
			return errors.New("null is not allowed in a spec")
		default:
			if top != nil && top.object {
				top.key = true
			}
		}
	}
	if values == 0 {
		return errors.New("the spec is empty")
	}
	return nil
}

// keys checks that every object key of v is a field of t at that place, in
// exact case. encoding/json matches field names without regard to case, so
// "IMAGE" would otherwise be read as "image".
func keys(v any, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not an object", path)
		}
		fields := map[string]reflect.Type{}
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			fields[name] = f.Type
		}
		for k, fv := range m {
			ft, ok := fields[k]
			if !ok {
				return fmt.Errorf("%s: unknown key %q", path, clip(k))
			}
			if err := keys(fv, ft, path+"."+k); err != nil {
				return err
			}
		}
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not an object", path)
		}
		for k, ev := range m {
			if err := keys(ev, t.Elem(), path+"."+clip(k)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		l, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s is not a list", path)
		}
		for i, ev := range l {
			if err := keys(ev, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// clip bounds input text quoted in a message.
func clip(s string) string {
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return strings.ToValidUTF8(s, "")
}
