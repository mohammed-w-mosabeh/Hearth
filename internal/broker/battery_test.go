package broker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func batterySys(t *testing.T) (root, limit string) {
	t.Helper()
	root = t.TempDir()
	add := func(name, kind string, has bool) string {
		dir := filepath.Join(root, "class", "power_supply", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "type"), []byte(kind+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, ChargeEndFile)
		if has {
			if err := os.WriteFile(f, []byte("100\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return f
	}
	limit = add("BAT0", "Battery", true)
	add("BAT1", "Battery", false)
	add("ACAD", "Mains", true)
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := add("BAT2", "Battery", false)
	if err := os.Symlink(filepath.Join(root, "secret"), link); err != nil {
		t.Fatal(err)
	}
	return root, limit
}

func TestBatteryThreshold(t *testing.T) {
	root, limit := batterySys(t)
	m := BatteryMethods(root)[0]
	if m.Name != "battery.threshold" || !m.Privileged || m.Action != ActionBatteryThreshold || m.Check == nil {
		t.Fatalf("%+v", m)
	}
	p := json.RawMessage(`{"battery":"BAT0","end":80}`)
	if err := m.Check(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Handle(context.Background(), Caller{}, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(limit); string(b) != "80" { // #nosec G304 -- a test temp file
		t.Fatalf("%q", b)
	}
}

func TestBatteryThresholdRefusals(t *testing.T) {
	root, limit := batterySys(t)
	m := BatteryMethods(root)[0]
	for _, p := range []string{
		`{"battery":"BAT0","end":49}`, `{"battery":"BAT0","end":101}`, `{"battery":"BAT0","end":0}`, `{"battery":"BAT0","end":-1}`,
		`{"battery":"BAT0","end":80.5}`, `{"battery":"BAT0","end":"80"}`, `{"battery":"BAT0"}`,
		`{"battery":"BAT1","end":80}`, `{"battery":"ACAD","end":80}`, `{"battery":"BAT2","end":80}`, `{"battery":"NOPE","end":80}`,
		`{"battery":"../../secret","end":80}`, `{"battery":"BAT0/..","end":80}`, `{"battery":"","end":80}`, `{"end":80}`,
		`{"battery":"BAT0\n","end":80}`, `{"battery":"BAT0","end":80,"path":"/etc/passwd"}`, `[]`, `"BAT0"`,
	} {
		if !isBad(m.Check(context.Background(), json.RawMessage(p))) {
			t.Errorf("passed Check: %s", p)
		}
		if _, err := m.Handle(context.Background(), Caller{}, json.RawMessage(p)); !isBad(err) {
			t.Errorf("handled %s: %v", p, err)
		}
	}
	if !isBad(m.Check(context.Background(), nil)) {
		t.Error("no params passed Check")
	}
	if b, _ := os.ReadFile(limit); string(b) != "100\n" { // #nosec G304 -- a test temp file
		t.Fatalf("a refused call wrote %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "secret")); string(b) != "x" { // #nosec G304 -- a test temp file
		t.Fatalf("followed a link: %q", b)
	}
}

func FuzzBatteryThreshold(f *testing.F) {
	root := f.TempDir()
	dir := filepath.Join(root, "class", "power_supply", "BAT0")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.Fatal(err)
	}
	for name, v := range map[string]string{"type": "Battery\n", ChargeEndFile: "100\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			f.Fatal(err)
		}
	}
	f.Add("BAT0", 80)
	f.Add("../x", 49)
	f.Fuzz(func(t *testing.T, name string, end int) {
		path, err := chargeEndPath(root, ThresholdParams{Battery: name, End: end})
		if err != nil {
			return
		}
		if end < MinChargeEnd || end > MaxChargeEnd || filepath.Dir(path) != dir {
			t.Fatalf("%q %s -> %s", name, strconv.Itoa(end), path)
		}
	})
}
