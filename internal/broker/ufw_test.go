package broker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

const userRules = `*filter
### RULES ###

### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0 in
-A ufw-user-input -p tcp --dport 22 -j ACCEPT

### tuple ### deny udp 53 0.0.0.0/0 any 10.0.0.0/8 in comment=6869
### tuple ### allow tcp 80 0.0.0.0/0 any 192.168.1.5 in
### tuple ### limit tcp 2222 0.0.0.0/0 any 0.0.0.0/0 in
### tuple ### deny_log tcp 25 0.0.0.0/0 any 0.0.0.0/0 in
### tuple ### allow tcp 8080 0.0.0.0/0 any 0.0.0.0/0 in_eth0
### tuple ### allow tcp 443 0.0.0.0/0 any 0.0.0.0/0 out
### tuple ### allow any 22 0.0.0.0/0 any 0.0.0.0/0 OpenSSH - in
### tuple ### route:allow tcp 80 0.0.0.0/0 any 0.0.0.0/0 in
### tuple ### allow tcp 1000:2000 0.0.0.0/0 any 0.0.0.0/0 in
### tuple ### allow tcp 9 10.0.0.1 any 0.0.0.0/0 in
### tuple ### bad
### END RULES ###
`

const user6Rules = `### tuple ### allow tcp 22 ::/0 any ::/0 in
### tuple ### allow tcp 8443 ::/0 any 2001:db8::/32 in
`

func TestCheckUfwRule(t *testing.T) {
	good := UfwRuleParams{Op: "add", Action: "allow", Proto: "tcp", Port: 443, Source: "10.0.0.0/8"}
	if v, err := CheckUfwRule(good); err != nil || v.Source != "10.0.0.0/8" {
		t.Fatalf("%+v %v", v, err)
	}
	canon := map[string]string{"": "", "192.168.1.5": "192.168.1.5", "192.168.1.5/32": "192.168.1.5", "2001:db8::/32": "2001:db8::/32", "2001:DB8::1": "2001:db8::1"}
	for in, want := range canon {
		if got, err := CleanUfwSource(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{
		"any", "0.0.0.0/0", "::/0", "10.0.0.1/8", "10.0.0.0/33", "fe80::1%eth0", "::ffff:1.2.3.4",
		"10.0.0.0/8 port 22", "10.0.0.0/8\nUFW_ARGS=disable", "--force", "$(id)", "1.2.3", " 10.0.0.1",
	} {
		if _, err := CleanUfwSource(bad); err == nil {
			t.Errorf("source %q accepted", bad)
		}
	}
	mutate := []func(*UfwRuleParams){
		func(p *UfwRuleParams) { p.Op = "insert" },
		func(p *UfwRuleParams) { p.Op = "" },
		func(p *UfwRuleParams) { p.Action = "reject" },
		func(p *UfwRuleParams) { p.Action = "limit" },
		func(p *UfwRuleParams) { p.Proto = "any" },
		func(p *UfwRuleParams) { p.Proto = "tcp log-all" },
		func(p *UfwRuleParams) { p.Port = 0 },
		func(p *UfwRuleParams) { p.Port = 65536 },
		func(p *UfwRuleParams) { p.Port = -1 },
	}
	for i, m := range mutate {
		p := good
		m(&p)
		if _, err := CheckUfwRule(p); err == nil {
			t.Errorf("case %d accepted: %+v", i, p)
		}
	}
}

func TestUfwArgs(t *testing.T) {
	got := strings.Join(ufwArgs(UfwRuleParams{Op: "add", Action: "deny", Proto: "udp", Port: 53}), " ")
	if got != "deny proto udp from any to any port 53" {
		t.Fatal(got)
	}
	got = strings.Join(ufwArgs(UfwRuleParams{Op: "delete", Action: "allow", Proto: "tcp", Port: 80, Source: "10.0.0.0/8"}), " ")
	if got != "delete allow proto tcp from 10.0.0.0/8 to any port 80" {
		t.Fatal(got)
	}
}

func TestParseUfwRules(t *testing.T) {
	v4, tr := parseUfwRules(strings.NewReader(userRules), "ipv4")
	v6, _ := parseUfwRules(strings.NewReader(user6Rules), "ipv6")
	if tr {
		t.Fatal("truncated")
	}
	rules := mergeUfwRules(v4, v6)
	editable := []string{}
	for _, r := range rules {
		if r.Editable {
			editable = append(editable, r.Action+"/"+r.Proto+"/"+r.Source+"/"+r.Family)
			if r.Text != "" {
				t.Errorf("%+v", r)
			}
		} else if r.Text == "" {
			t.Errorf("a rule that cannot be edited must still read: %+v", r)
		}
	}
	want := "allow/tcp//both deny/udp/10.0.0.0/8/ipv4 allow/tcp/192.168.1.5/ipv4 allow/tcp/2001:db8::/32/ipv6"
	if strings.Join(editable, " ") != want {
		t.Fatalf("%v", editable)
	}
	if n := len(rules) - len(editable); n != 8 {
		t.Fatalf("%d read-only rules: %+v", n, rules)
	}
}

func TestParseUfwRulesIsBounded(t *testing.T) {
	var b strings.Builder
	for range maxUfwRules + 5 {
		b.WriteString("### tuple ### allow tcp 1 0.0.0.0/0 any 0.0.0.0/0 in\n")
	}
	r, tr := parseUfwRules(strings.NewReader(b.String()), "ipv4")
	if len(r) != maxUfwRules || !tr {
		t.Fatalf("%d %v", len(r), tr)
	}
}

func ufwEnv(t *testing.T, active bool) (UfwConfig, *lynisBus, map[string]Method) {
	t.Helper()
	dir := t.TempDir()
	conf := filepath.Join(dir, "ufw")
	if err := os.Mkdir(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	enabled := "no"
	if active {
		enabled = "yes"
	}
	for name, body := range map[string]string{
		"ufw-bin":        "",
		"ufw/ufw.conf":   "# comment\nENABLED=" + enabled + "\n",
		"ufw/user.rules": userRules, "ufw/user6.rules": user6Rules,
		"defaults": "DEFAULT_INPUT_POLICY=\"DROP\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bus := &lynisBus{state: "inactive"}
	c := UfwConfig{
		Bus:    func(context.Context) (SystemBus, error) { return bus, nil },
		Binary: filepath.Join(dir, "ufw-bin"), ConfigDir: conf, Defaults: filepath.Join(dir, "defaults"),
		Args: filepath.Join(dir, "ufw.args"), Poll: time.Millisecond,
	}
	m := map[string]Method{}
	for _, x := range UfwMethods(c) {
		m[x.Name] = x
	}
	return c, bus, m
}

func TestUfwMethodsAreClosed(t *testing.T) {
	_, _, m := ufwEnv(t, true)
	if len(m) != 3 {
		t.Fatalf("%d methods", len(m))
	}
	for _, n := range []string{"ufw.rule", "ufw.set"} {
		if !m[n].Privileged || m[n].Action != ActionFirewall || m[n].Check == nil {
			t.Fatalf("%s must ask polkit, after its params: %+v", n, m[n])
		}
	}
	if m["ufw.state"].Privileged {
		t.Fatal("reading must raise no dialog")
	}
	ctx := context.Background()
	for _, bad := range []string{
		`{}`,
		`{"op":"add","action":"allow","proto":"tcp","port":0}`,
		`{"op":"add","action":"allow","proto":"tcp","port":22,"args":"--force"}`,
		`{"op":"add","action":"allow","proto":"tcp","port":22,"interface":"eth0"}`,
		`{"op":"reset"}`, `{"op":"add","action":"allow","proto":"tcp","port":"22"}`,
		`{"op":"add","action":"allow","proto":"tcp","port":22,"source":"any to any"}`,
	} {
		if err := m["ufw.rule"].Check(ctx, json.RawMessage(bad)); err == nil {
			t.Errorf("ufw.rule took %s", bad)
		}
	}
	for _, bad := range []string{`{"enabled":"yes"}`, `{"enabled":true,"force":true}`, `[]`} {
		if err := m["ufw.set"].Check(ctx, json.RawMessage(bad)); err == nil {
			t.Errorf("ufw.set took %s", bad)
		}
	}
	if err := m["ufw.set"].Check(ctx, nil); err == nil {
		t.Error("ufw.set took nothing")
	}
}

// The args file holds what the unit will run, and goes when it is done.
func TestUfwRunWritesOneLine(t *testing.T) {
	c, bus, m := ufwEnv(t, true)
	var seen string
	bus.onStart = func() {
		b, err := os.ReadFile(c.Args)
		if err != nil {
			t.Error(err)
		}
		seen = string(b)
		fi, err := os.Stat(c.Args)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%v %v", fi, err)
		}
	}
	p := json.RawMessage(`{"op":"delete","action":"deny","proto":"udp","port":53,"source":"10.0.0.0/8"}`)
	if _, err := m["ufw.rule"].Handle(context.Background(), Caller{}, p); err != nil {
		t.Fatal(err)
	}
	if seen != "UFW_ARGS=delete deny proto udp from 10.0.0.0/8 to any port 53\n" {
		t.Fatalf("%q", seen)
	}
	if _, err := os.Stat(c.Args); !os.IsNotExist(err) {
		t.Fatal("the args file stayed")
	}
	for enabled, want := range map[string]string{"true": "UFW_ARGS=--force enable\n", "false": "UFW_ARGS=disable\n"} {
		seen = ""
		if _, err := m["ufw.set"].Handle(context.Background(), Caller{}, json.RawMessage(`{"enabled":`+enabled+`}`)); err != nil || seen != want {
			t.Fatalf("%q %v", seen, err)
		}
	}
}

func TestUfwRuleNeedsAnActiveFirewall(t *testing.T) {
	_, bus, m := ufwEnv(t, false)
	p := json.RawMessage(`{"op":"add","action":"allow","proto":"tcp","port":22}`)
	if _, err := m["ufw.rule"].Handle(context.Background(), Caller{}, p); err == nil {
		t.Fatal("a rule on an inactive firewall")
	}
	if len(bus.calls) != 0 {
		t.Fatalf("%v", bus.calls)
	}
}

func TestUfwState(t *testing.T) {
	c, _, m := ufwEnv(t, true)
	v, err := m["ufw.state"].Handle(context.Background(), Caller{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := v.(UfwState)
	if !s.Installed || !s.Active || s.Incoming != "deny" || len(s.Rules) != 12 {
		t.Fatalf("%+v", s)
	}
	if err := os.Remove(c.Binary); err != nil {
		t.Fatal(err)
	}
	v, _ = m["ufw.state"].Handle(context.Background(), Caller{}, nil)
	if s := v.(UfwState); s.Installed || s.Rules == nil {
		t.Fatalf("%+v", s)
	}
}

// Whatever passes must be safe to put on one line for systemd to split.
func FuzzCheckUfwRule(f *testing.F) {
	f.Add("add", "allow", "tcp", 22, "10.0.0.0/8")
	f.Add("delete", "deny", "udp", 65535, "2001:db8::1")
	f.Add("add", "allow", "tcp", 1, "10.0.0.0/8 x")
	f.Fuzz(func(t *testing.T, op, action, proto string, port int, source string) {
		v, err := CheckUfwRule(UfwRuleParams{Op: op, Action: action, Proto: proto, Port: port, Source: source})
		if err != nil {
			return
		}
		for _, a := range ufwArgs(v) {
			if a == "" || strings.IndexFunc(a, func(r rune) bool {
				return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`$"'\;&|<>`+"`", r)
			}) >= 0 {
				t.Fatalf("unsafe word %q in %+v", a, v)
			}
		}
	})
}

func FuzzParseUfwRules(f *testing.F) {
	f.Add(userRules)
	f.Add(user6Rules)
	f.Add("### tuple ### a b c d e f g h i j\n### tuple ### \n")
	f.Fuzz(func(t *testing.T, s string) {
		rules, _ := parseUfwRules(strings.NewReader(s), "ipv4")
		for _, r := range rules {
			if r.Editable {
				if _, err := CheckUfwRule(UfwRuleParams{Op: "delete", Action: r.Action, Proto: r.Proto, Port: r.Port, Source: r.Source}); err != nil {
					t.Fatalf("an editable rule cannot be deleted: %+v %v", r, err)
				}
			} else if strings.ContainsAny(r.Text, "\x00\n\x1b") {
				t.Fatalf("%q", r.Text)
			}
		}
	})
}
