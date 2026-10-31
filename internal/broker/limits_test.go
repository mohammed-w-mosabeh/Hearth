package broker

import (
	"strings"
	"testing"
)

func TestParseBytes(t *testing.T) {
	for s, want := range map[string]int64{"1": 1, "512M": 512 << 20, "2G": 2 << 30, "1T": 1 << 40, "4K": 4096} {
		if got, err := ParseBytes(s); err != nil || got != want {
			t.Errorf("%s: %d %v", s, got, err)
		}
	}
	for _, s := range []string{"", "0", "-1", "1g", "1 G", "1.5G", "1P", "999999999999999T", "1\n"} {
		if _, err := ParseBytes(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestParseCPUs(t *testing.T) {
	for s, want := range map[string]int64{"1": 1e9, "1.5": 15e8, "0.01": 1e7, "0.5": 5e8, "1024": 1024e9} {
		if got, err := ParseCPUs(s); err != nil || got != want {
			t.Errorf("%s: %d %v", s, got, err)
		}
	}
	for _, s := range []string{"", "0", "0.00", "1025", "1.555", ".5", "-1", "1e3", "1,5"} {
		if _, err := ParseCPUs(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestContainerUpdate(t *testing.T) {
	h, f, _ := dockerHarness(t, true, map[string]answer{"POST /containers/web/update": {200, `{"Warnings":[]}`}})
	for _, p := range []string{
		`{"id":"web"}`,
		`{"Image":"alpine","HostConfig":{"Privileged":true,"Binds":["/:/host"]}}`,
		`{"id":"web","HostConfig":{"Privileged":true,"Binds":["/:/host"]}}`,
		`{"id":"web","privileged":true}`,
		`{"id":"web","memory":1048576,"Privileged":true}`,
		`{"id":"web","CpusetCpus":"0"}`,
		`{"id":"web","Devices":[{"PathOnHost":"/dev/sda"}]}`,
		`{"id":"web","CapAdd":["ALL"]}`,
		`{"id":"web","memory":1}`,
		`{"id":"web","memory":-1}`,
		`{"id":"web","nano_cpus":1}`,
		`{"id":"web","nano_cpus":-1}`,
		`{"id":"web","restart":"sometimes"}`,
		`{"id":"web","restart":"always\n"}`,
		`{"id":"../x","memory":10485760}`,
		`{"memory":10485760}`,
	} {
		if err := call(t, h, "docker.containers.update", p, nil); code(err) != CodeBadParams {
			t.Errorf("%s: %v", p, err)
		}
	}
	if len(f.requests()) != 0 {
		t.Fatalf("refused calls reached the Engine: %v", f.requests())
	}
	if err := call(t, h, "docker.containers.update", `{"id":"web","memory":10485760,"nano_cpus":1500000000,"restart":"always"}`, nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0], "POST /containers/web/update") {
		t.Fatalf("%v", reqs)
	}
}

func TestContainerUpdateNeedsPolkit(t *testing.T) {
	h, f, _ := dockerHarness(t, false, nil)
	if err := call(t, h, "docker.containers.update", `{"id":"web","memory":10485760}`, nil); code(err) != CodeUnauthorized {
		t.Fatalf("%v", err)
	}
	if len(f.requests()) != 0 {
		t.Fatalf("%v", f.requests())
	}
}

func FuzzParseBytes(f *testing.F) {
	for _, s := range []string{"512M", "999999999999999T", "1\n", "-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if n, err := ParseBytes(s); err == nil && (n <= 0 || n > maxMemory) {
			t.Fatalf("%q -> %d", s, n)
		}
	})
}

func FuzzParseCPUs(f *testing.F) {
	for _, s := range []string{"1.5", "0.01", "9999", "1\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if n, err := ParseCPUs(s); err == nil && (n < minNanoCPUs || n > maxNanoCPUs) {
			t.Fatalf("%q -> %d", s, n)
		}
	})
}

func FuzzUpdateParams(f *testing.F) {
	f.Add("web", int64(10485760), int64(1500000000), "always")
	f.Add("../x", int64(1), int64(-1), "sometimes")
	f.Fuzz(func(t *testing.T, id string, mem, cpus int64, restart string) {
		v := UpdateParams{ID: id, Memory: mem, NanoCPUs: cpus, Restart: restart}
		if v.validate() != nil {
			return
		}
		if strings.ContainsAny(id+restart, "/\n\x00") || mem < 0 || cpus < 0 {
			t.Fatalf("accepted %+v", v)
		}
	})
}
