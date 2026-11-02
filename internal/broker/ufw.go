package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ActionFirewall is the polkit action for changing the firewall. It is
	// auth_admin with no keep: a port opened here faces the network.
	ActionFirewall = "org.hearth.firewall.manage"
	// UfwUnit runs ufw. It is a unit of its own so the broker holds neither
	// CAP_NET_ADMIN nor a netlink socket (SECURITY S16h).
	UfwUnit = "hearth-ufw.service"
	// UfwArgs is the unit's EnvironmentFile: one line, built by ufwArgs.
	UfwArgs = "/var/lib/hearth/ufw.args"
	// UfwBinary tells whether ufw is there.
	UfwBinary = "/usr/sbin/ufw"
	// UfwConfigDir holds ufw.conf and the rules ufw wrote.
	UfwConfigDir = "/etc/ufw"
	// UfwDefaults holds the default policies.
	UfwDefaults = "/etc/default/ufw"

	maxUfwFile  = 1 << 20
	maxUfwRules = 1000
	maxUfwText  = 200
	anyV4       = "0.0.0.0/0"
	anyV6       = "::/0"
)

// UfwRuleParams are ufw.rule's params: the whole of what can be asked.
type UfwRuleParams struct {
	Op     string `json:"op"`     // add or delete
	Action string `json:"action"` // allow or deny
	Proto  string `json:"proto"`  // tcp or udp
	Port   int    `json:"port"`   // 1 to 65535
	Source string `json:"source"` // an address or CIDR; empty is anywhere
}

// UfwSetParams are ufw.set's params.
type UfwSetParams struct {
	Enabled bool `json:"enabled"`
}

// UfwRule is one rule. Only a plain inbound port rule is Editable, and only
// such a rule can be deleted here; any other is shown as Text.
type UfwRule struct {
	Action   string `json:"action"`
	Proto    string `json:"proto"`
	Port     int    `json:"port,omitempty"`
	Source   string `json:"source,omitempty"`
	Family   string `json:"family"` // ipv4, ipv6 or both
	Editable bool   `json:"editable"`
	Text     string `json:"text,omitempty"`
}

// UfwState is ufw.state's result.
type UfwState struct {
	Installed bool      `json:"installed"`
	Active    bool      `json:"active"`
	Incoming  string    `json:"incoming,omitempty"` // the default policy, lower case
	Rules     []UfwRule `json:"rules"`
	Truncated bool      `json:"truncated,omitempty"`
}

// CleanUfwSource canonicalizes a source. Empty is anywhere; "/0" is refused
// so the same rule cannot be written two ways; host bits past the mask are
// refused, since they would not mean what they say.
func CleanUfwSource(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	bad := errors.New("source must be an IP address or CIDR, like 192.168.1.0/24")
	if !strings.Contains(s, "/") {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" || a.Is4In6() {
			return "", bad
		}
		return a.String(), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || p.Addr().Zone() != "" || p.Addr().Is4In6() || p.Masked() != p {
		return "", bad
	}
	if p.Bits() == 0 {
		return "", errors.New("leave the source empty for anywhere")
	}
	if p.IsSingleIP() {
		return p.Addr().String(), nil
	}
	return p.String(), nil
}

// CheckUfwRule validates params and returns them canonical. Nothing else
// reaches ufw's command line.
func CheckUfwRule(p UfwRuleParams) (UfwRuleParams, error) {
	if p.Op != "add" && p.Op != "delete" {
		return p, errors.New("op must be add or delete")
	}
	if p.Action != "allow" && p.Action != "deny" {
		return p, errors.New("action must be allow or deny")
	}
	if p.Proto != "tcp" && p.Proto != "udp" {
		return p, errors.New("proto must be tcp or udp")
	}
	if p.Port < 1 || p.Port > 65535 {
		return p, errors.New("port must be 1 to 65535")
	}
	src, err := CleanUfwSource(p.Source)
	if err != nil {
		return p, err
	}
	p.Source = src
	return p, nil
}

// ufwArgs is the command line of a checked rule.
func ufwArgs(p UfwRuleParams) []string {
	src := p.Source
	if src == "" {
		src = "any"
	}
	args := []string{p.Action, "proto", p.Proto, "from", src, "to", "any", "port", strconv.Itoa(p.Port)}
	if p.Op == "delete" {
		args = append([]string{"delete"}, args...)
	}
	return args
}

// parseUfwTuple reads what follows "### tuple ### " in user.rules: action,
// proto, dport, dst, sport, src, then optionally dapp and sapp, then the
// direction. The comment= field was cut off first.
func parseUfwTuple(s, family string) (UfwRule, bool) {
	f := strings.Fields(s)
	if len(f) < 6 || len(f) > 9 {
		return UfwRule{}, false
	}
	r := UfwRule{Action: cleanText(f[0], 32), Proto: cleanText(f[1], 16), Family: family}
	r.Text = cleanText(strings.Join(f, " "), maxUfwText)
	dir := "in"
	if len(f) == 7 || len(f) == 9 {
		dir = f[len(f)-1]
	}
	apps := len(f) >= 8 && (f[6] != "-" || f[7] != "-")
	src := f[5]
	if src == anyV4 || src == anyV6 {
		src = ""
	}
	port, err := strconv.Atoi(f[2])
	clean, cerr := CleanUfwSource(src)
	if (r.Action == "allow" || r.Action == "deny") && (r.Proto == "tcp" || r.Proto == "udp") && dir == "in" && !apps &&
		(f[3] == anyV4 || f[3] == anyV6) && f[4] == "any" && err == nil && port >= 1 && port <= 65535 && cerr == nil {
		r.Port, r.Source, r.Editable, r.Text = port, clean, true, ""
	}
	return r, true
}

// parseUfwRules reads one rules file; lines ufw itself would skip are
// skipped. The file's rules keep ufw's order.
func parseUfwRules(r io.Reader, family string) (rules []UfwRule, truncated bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "### tuple ### ")
		if !ok {
			continue
		}
		line, _, _ = strings.Cut(line, " comment=")
		rule, ok := parseUfwTuple(line, family)
		if !ok {
			continue
		}
		if len(rules) >= maxUfwRules {
			return rules, true
		}
		rules = append(rules, rule)
	}
	return rules, false
}

// mergeUfwRules folds a rule both files hold, which is what `from any`
// writes, into one row: deleting it by its text removes both.
func mergeUfwRules(v4, v6 []UfwRule) []UfwRule {
	out := make([]UfwRule, 0, len(v4)+len(v6))
	type key struct {
		a, p string
		n    int
		s    string
	}
	at := map[key]int{}
	for _, r := range v4 {
		if r.Editable && r.Source == "" {
			at[key{r.Action, r.Proto, r.Port, ""}] = len(out)
		}
		out = append(out, r)
	}
	for _, r := range v6 {
		if i, ok := at[key{r.Action, r.Proto, r.Port, r.Source}]; ok && r.Editable && r.Source == "" {
			out[i].Family = "both"
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Editable != out[j].Editable {
			return out[i].Editable
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// UfwConfig configures UfwMethods.
type UfwConfig struct {
	Bus       func(ctx context.Context) (SystemBus, error)
	Binary    string // default UfwBinary
	ConfigDir string // default UfwConfigDir
	Defaults  string // default UfwDefaults
	Args      string // default UfwArgs
	Poll      time.Duration
}

// value reads KEY=value from a shell-style file, without its quotes.
func ufwValue(path, key string) string {
	f, err := os.Open(path) // #nosec G304 -- fixed paths
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(io.LimitReader(f, maxUfwFile))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), key+"="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func (c UfwConfig) readRules(name, family string) ([]UfwRule, bool) {
	f, err := os.Open(filepath.Join(c.ConfigDir, name)) // #nosec G304 -- fixed names under a fixed directory
	if err != nil {
		return nil, false // no IPv6 file on a machine without it
	}
	defer func() { _ = f.Close() }()
	return parseUfwRules(io.LimitReader(f, maxUfwFile), family)
}

func (c UfwConfig) state() UfwState {
	s := UfwState{Rules: []UfwRule{}}
	if _, err := os.Stat(c.Binary); err != nil {
		return s
	}
	s.Installed = true
	s.Active = ufwValue(filepath.Join(c.ConfigDir, "ufw.conf"), "ENABLED") == "yes"
	s.Incoming = map[string]string{"DROP": "deny", "REJECT": "reject", "ACCEPT": "allow"}[ufwValue(c.Defaults, "DEFAULT_INPUT_POLICY")]
	v4, t4 := c.readRules("user.rules", "ipv4")
	v6, t6 := c.readRules("user6.rules", "ipv6")
	s.Rules, s.Truncated = mergeUfwRules(v4, v6), t4 || t6
	return s
}

// run writes the unit's one line and starts it. The line is built here and
// nowhere else; systemd splits it into words, without a shell.
func (c UfwConfig) run(ctx context.Context, args []string) error {
	_ = os.Remove(c.Args)
	// O_EXCL: a link in the way is refused, not followed.
	f, err := os.OpenFile(c.Args, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a fixed path in the 0700 state directory
	if err != nil {
		return Errorf(CodeFailed, "ufw: could not prepare the change")
	}
	defer func() { _ = os.Remove(c.Args) }()
	_, werr := f.WriteString("UFW_ARGS=" + strings.Join(args, " ") + "\n")
	if cerr := f.Close(); werr != nil || cerr != nil {
		return Errorf(CodeFailed, "ufw: could not prepare the change")
	}
	return runOneshot(ctx, c.Bus, c.Poll, "ufw", UfwUnit)
}

// UfwMethods are the broker's firewall methods (SECURITY S16h). ufw.state
// reads ufw's own files, runs nothing and needs no polkit. ufw.rule and
// ufw.set ask polkit after their params pass. Rules are written only while
// ufw is active, so an inactive firewall cannot read as protecting anything.
func UfwMethods(c UfwConfig) []Method {
	if c.Binary == "" {
		c.Binary = UfwBinary
	}
	if c.ConfigDir == "" {
		c.ConfigDir = UfwConfigDir
	}
	if c.Defaults == "" {
		c.Defaults = UfwDefaults
	}
	if c.Args == "" {
		c.Args = UfwArgs
	}
	var mu sync.Mutex // one change at a time: there is one args file
	rule := func(p json.RawMessage) (UfwRuleParams, error) {
		var v UfwRuleParams
		if err := decodeParams(p, &v); err != nil {
			return v, err
		}
		v, err := CheckUfwRule(v)
		if err != nil {
			return v, badParam("%s", err.Error())
		}
		return v, nil
	}
	set := func(p json.RawMessage) (UfwSetParams, error) {
		var v UfwSetParams
		if len(p) == 0 {
			return v, badParam("enabled is required")
		}
		return v, decodeParams(p, &v)
	}
	missing := func(err error) bool { return errors.Is(err, fs.ErrNotExist) }
	return []Method{{
		Name: "ufw.state",
		Handle: func(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
			if err := noParams(p); err != nil {
				return nil, err
			}
			return c.state(), nil
		},
	}, {
		Name: "ufw.rule", Privileged: true, Action: ActionFirewall, Timeout: 2 * time.Minute,
		Check: func(_ context.Context, p json.RawMessage) error { _, err := rule(p); return err },
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			v, err := rule(p)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			if _, err := os.Stat(c.Binary); missing(err) {
				return nil, Errorf(CodeFailed, "ufw is not installed")
			}
			if !c.state().Active {
				return nil, Errorf(CodeRefused, "ufw is not active")
			}
			if err := c.run(ctx, ufwArgs(v)); err != nil {
				return nil, err
			}
			return map[string]bool{"done": true}, nil
		},
	}, {
		Name: "ufw.set", Privileged: true, Action: ActionFirewall, Timeout: 2 * time.Minute,
		Check: func(_ context.Context, p json.RawMessage) error { _, err := set(p); return err },
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			v, err := set(p)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			if _, err := os.Stat(c.Binary); missing(err) {
				return nil, Errorf(CodeFailed, "ufw is not installed")
			}
			args := []string{"disable"}
			if v.Enabled {
				args = []string{"--force", "enable"} // --force only skips the terminal's question
			}
			if err := c.run(ctx, args); err != nil {
				return nil, err
			}
			return map[string]bool{"enabled": v.Enabled}, nil
		},
	}}
}
