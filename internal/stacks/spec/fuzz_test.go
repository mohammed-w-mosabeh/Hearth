package spec

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// FuzzParse: Parse never panics; a spec it accepts marshals back to one it
// accepts unchanged, holds nothing S12b never allows, and uses no flag
// without its opt-in.
func FuzzParse(f *testing.F) {
	f.Add([]byte(good))
	f.Add([]byte(`{"version":1,"name":"a","services":{"a":{"image":"nginx"}}}`))
	f.Add([]byte(`{"version":1,"name":"a","services":{"a":{"image":"nginx","volumes":[{"type":"bind","source":"/proc/self/root","target":"/x"}]}}}`))
	f.Add([]byte(`{"version":1,"name":"a","services":{"a":{"image":"nginx","cap_add":["SYS_ADMIN"]}},"opt_in":["cap:SYS_ADMIN"]}`))
	f.Add([]byte(`{"version":1,"name":"a","services":{"a":{"image":"nginx","IMAGE":"x"}}}`))
	f.Add([]byte("services:\n  a: &x\n    image: nginx\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		s, r, err := Parse(raw)
		if err != nil || !r.OK() {
			return
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		s2, r2, err := Parse(b)
		if err != nil || !r2.OK() || !slices.Equal(r.Flags, r2.Flags) {
			t.Fatalf("accepted spec does not read back: %v %q", err, r2.Refused)
		}
		if b2, _ := json.Marshal(s2); string(b2) != string(b) {
			t.Fatal("round trip changed the spec")
		}
		for _, fl := range r.Flags {
			if !slices.Contains(s.OptIn, fl) {
				t.Fatalf("flag %s without opt-in", fl)
			}
		}
		for name, svc := range s.Services {
			for _, m := range svc.Volumes {
				if m.Type == "bind" && (neverBind(m.Source) != "" || strings.Contains(m.Source, "..")) {
					t.Fatalf("%s: bind of %s accepted", name, m.Source)
				}
			}
			for _, c := range svc.CapAdd {
				if neverCaps[strings.TrimPrefix(c, "CAP_")] {
					t.Fatalf("%s: cap %s accepted", name, c)
				}
			}
			for _, d := range svc.Devices {
				host, _, _ := strings.Cut(d, ":")
				if deviceKind(host) != deviceListed {
					t.Fatalf("%s: device %s accepted", name, d)
				}
			}
			for _, o := range svc.SecurityOpt {
				if strings.Contains(o, "unconfined") {
					t.Fatalf("%s: security_opt %s accepted", name, o)
				}
			}
		}
	})
}
