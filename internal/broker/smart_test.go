package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const ataOK = `{"smartctl":{"exit_status":0},"device":{"name":"/dev/sda","protocol":"ATA"},
 "model_name":"Samsung SSD 870","serial_number":"S1SECRET","wwn":{"naa":5,"oui":9528,"id":1},
 "user_capacity":{"blocks":1000,"bytes":512000},"rotation_rate":0,
 "smart_support":{"available":true,"enabled":true},"smart_status":{"passed":true},
 "temperature":{"current":31},"power_on_time":{"hours":1234},
 "ata_smart_attributes":{"table":[
  {"id":5,"name":"Reallocated_Sector_Ct","value":100,"raw":{"value":2},"when_failed":""},
  {"id":177,"name":"Wear_Leveling_Count","value":97,"raw":{"value":40},"when_failed":""},
  {"id":197,"name":"Current_Pending_Sector","value":100,"raw":{"value":0},"when_failed":""}]},
 "ata_smart_self_test_log":{"standard":{"table":[
  {"type":{"value":1,"string":"Short offline"},"status":{"value":0,"string":"Completed without error","passed":true},"lifetime_hours":1200}]}}}`

const nvmeOK = `{"device":{"protocol":"NVMe"},"model_name":"WD Blue","nvme_total_capacity":1024,
 "smart_status":{"passed":true},"temperature":{"current":40},"power_on_time":{"hours":10},
 "nvme_smart_health_information_log":{"percentage_used":3,"media_errors":0},
 "nvme_self_test_log":{"current_self_test_operation":{"value":1,"string":"Short"},"current_self_test_completion_percent":40,
 "table":[{"self_test_code":{"value":1,"string":"Short"},"self_test_result":{"value":7,"string":"Completed: failed segments"},"power_on_hours":9}]}}`

func report(t *testing.T, out string, code int) SmartReport {
	t.Helper()
	r, err := parseSmart("sda", safeexec.Result{Stdout: []byte(out), ExitCode: code})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseATA(t *testing.T) {
	r := report(t, ataOK, 0)
	if !r.Supported || r.Failing || r.Model != "Samsung SSD 870" || r.Protocol != "ATA" || r.Capacity != 512000 ||
		*r.TempC != 31 || *r.PowerOnHours != 1234 || *r.Reallocated != 2 || *r.Pending != 0 || *r.LifePct != 97 || r.Testing {
		t.Fatalf("%+v", r)
	}
	if r.LastTest == nil || !r.LastTest.Passed || r.LastTest.Kind != "Short offline" || *r.LastTest.Hours != 1200 {
		t.Fatalf("%+v", r.LastTest)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "S1SECRET") || strings.Contains(string(b), "wwn") {
		t.Fatalf("serial left the broker: %s", b)
	}
}

func TestParseNVMe(t *testing.T) {
	r := report(t, nvmeOK, 0)
	if *r.LifePct != 97 || *r.MediaErrors != 0 || r.Capacity != 1024 || !r.Testing || *r.Remaining != 60 {
		t.Fatalf("%+v", r)
	}
	if r.LastTest == nil || r.LastTest.Passed {
		t.Fatalf("%+v", r.LastTest)
	}
}

func TestFailing(t *testing.T) {
	if !report(t, `{"smart_status":{"passed":false}}`, 8).Failing {
		t.Error("a failed status is not failing")
	}
	if !report(t, ataOK, smartPrefail).Failing {
		t.Error("exit bit 4 is not failing")
	}
	if !report(t, `{"smart_status":{"passed":true},"ata_smart_attributes":{"table":[{"id":5,"value":1,"raw":{"value":1},"when_failed":"now"}]}}`, 0).Failing {
		t.Error("an attribute failing now is not failing")
	}
	if report(t, ataOK, 64|128).Failing || report(t, ataOK, 32).Failing {
		t.Error("error-log and past-threshold bits are not the disk's verdict")
	}
}

func TestLifeOnlyOnSSD(t *testing.T) {
	spinning := strings.Replace(ataOK, `"rotation_rate":0`, `"rotation_rate":7200`, 1)
	if r := report(t, spinning, 0); r.LifePct != nil {
		t.Fatalf("life on a rotating disk: %d", *r.LifePct)
	}
	over := `{"nvme_smart_health_information_log":{"percentage_used":120}}`
	if r := report(t, over, 0); *r.LifePct != 0 {
		t.Fatalf("life past 100%% used: %d", *r.LifePct)
	}
}

func TestStandbyAndFailures(t *testing.T) {
	r := report(t, `{"smartctl":{"messages":[{"string":"Device is in STANDBY mode, exit(2)","severity":"information"}],"exit_status":2}}`, 2)
	if !r.Asleep || r.Supported {
		t.Fatalf("%+v", r)
	}
	for _, c := range []struct {
		out  string
		code int
	}{
		{`{"smartctl":{"messages":[{"string":"/dev/sda: Unable to detect device type","severity":"error"}]}}`, 1},
		{`{"smartctl":{"messages":[{"string":"Smartctl open device: /dev/sda failed: Permission denied"}]}}`, 2},
		{`not json`, 0},
	} {
		if _, err := parseSmart("sda", safeexec.Result{Stdout: []byte(c.out), ExitCode: c.code}); err == nil {
			t.Errorf("no error for %q", c.out)
		}
	}
	if _, err := parseSmart("sda", safeexec.Result{Stdout: []byte(ataOK), Truncated: true}); err == nil {
		t.Error("truncated output accepted")
	}
	if report(t, `{"device":{"protocol":"ATA"},"smart_support":{"available":false}}`, 4).Supported {
		t.Error("an unsupported disk reads as supported")
	}
}

func TestHostileModel(t *testing.T) {
	r := report(t, `{"model_name":"<img src=x>\u0007\u001b[31m`+strings.Repeat("A", 200)+`"}`, 0)
	if strings.ContainsRune(r.Model, 7) || strings.ContainsRune(r.Model, 27) || len(r.Model) > 64 {
		t.Fatalf("%q", r.Model)
	}
}

func smartSys(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"block/sda/device", "block/nvme0n1/device"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func method(t *testing.T, ms []Method, name string) Method {
	t.Helper()
	for _, m := range ms {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no method %s", name)
	return Method{}
}

func TestSmartMethods(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		calls = append(calls, append([]string{name}, args...))
		if err := safeexec.Check(name, args...); err != nil {
			t.Errorf("safeexec would refuse %q: %v", args, err)
		}
		return safeexec.Result{Stdout: []byte(ataOK)}, nil
	}
	ms := SmartMethods(run, smartSys(t))
	read, test := method(t, ms, "disk.smart"), method(t, ms, "disk.selftest")
	if read.Privileged {
		t.Error("disk.smart would raise a password dialog on every poll")
	}
	if !test.Privileged || test.Action != ActionDiskTest || test.Check == nil {
		t.Errorf("%+v", test)
	}
	res, err := read.Handle(context.Background(), Caller{}, json.RawMessage(`{"device":"sda"}`))
	if err != nil || !res.(SmartReport).Supported {
		t.Fatal(res, err)
	}
	if _, err := test.Handle(context.Background(), Caller{}, json.RawMessage(`{"device":"nvme0n1","kind":"long"}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls[0], " ") != "smartctl-read -j -a -n standby /dev/sda" || strings.Join(calls[1], " ") != "smartctl-test -j -t long /dev/nvme0n1" {
		t.Fatalf("%q", calls)
	}
}

// Every device name and kind outside the allowlist is refused before
// polkit and before anything runs (Gate 5).
func TestSmartRefusals(t *testing.T) {
	run := func(context.Context, string, ...string) (safeexec.Result, error) {
		t.Fatal("ran a command")
		return safeexec.Result{}, nil
	}
	ms := SmartMethods(run, smartSys(t))
	read, test := method(t, ms, "disk.smart"), method(t, ms, "disk.selftest")
	devices := []string{"", "sda1", "sdb", "nvme0n1p1", "nvme1n1", "loop0", "sr0", "../../etc/shadow", "/dev/sda", "/dev/mem",
		"-a", "--scan", "sda\n", "sda;id", "sda ", "$(id)", "SDA", strings.Repeat("a", 5000)}
	for _, d := range devices {
		p, _ := json.Marshal(SmartParams{Device: d})
		if _, err := read.Handle(context.Background(), Caller{}, p); !isBad(err) {
			t.Errorf("disk.smart %q: %v", d, err)
		}
		p, _ = json.Marshal(SelfTestParams{Device: d, Kind: "short"})
		if !isBad(test.Check(context.Background(), p)) {
			t.Errorf("disk.selftest %q passed Check", d)
		}
		if _, err := test.Handle(context.Background(), Caller{}, p); !isBad(err) {
			t.Errorf("disk.selftest %q: %v", d, err)
		}
	}
	for _, p := range []string{`{"device":"sda","kind":"offline"}`, `{"device":"sda","kind":"force"}`, `{"device":"sda","kind":""}`, `{"device":"sda"}`,
		`{"device":"sda","kind":"short","extra":1}`, `{"kind":"short"}`, `[]`, `"sda"`} {
		if !isBad(test.Check(context.Background(), json.RawMessage(p))) {
			t.Errorf("disk.selftest passed Check: %s", p)
		}
	}
	if _, err := read.Handle(context.Background(), Caller{}, json.RawMessage(`{"device":"sda","x":1}`)); !isBad(err) {
		t.Errorf("unknown field: %v", err)
	}
}

func isBad(err error) bool {
	var be *Error
	return errors.As(err, &be) && be.Code == CodeBadParams
}

func TestSelfTestFailureAndMissingTool(t *testing.T) {
	sys := smartSys(t)
	running := SmartMethods(func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{ExitCode: 4, Stdout: []byte(`{"smartctl":{"messages":[{"string":"Can't start self-test without aborting current test"}]}}`)}, errExit
	}, sys)
	_, err := method(t, running, "disk.selftest").Handle(context.Background(), Caller{}, json.RawMessage(`{"device":"sda","kind":"short"}`))
	if err == nil || !strings.Contains(err.Error(), "aborting current test") {
		t.Fatalf("%v", err)
	}
	missing := SmartMethods(func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{}, safeexec.ErrBinary
	}, sys)
	if _, err := method(t, missing, "disk.smart").Handle(context.Background(), Caller{}, json.RawMessage(`{"device":"sda"}`)); !errors.Is(err, ErrNoSmartctl) {
		t.Fatalf("%v", err)
	}
}

// errExit is what safeexec.Run returns with any non-zero exit status.
var errExit = errors.New("safeexec: /usr/sbin/smartctl exited with a status")

// smartctl's exit status is data: these all exit non-zero through
// safeexec, which reports that as an error.
func TestNonZeroExitIsAReport(t *testing.T) {
	sys := smartSys(t)
	read := func(out string, code int, err error) (any, error) {
		ms := SmartMethods(func(context.Context, string, ...string) (safeexec.Result, error) {
			return safeexec.Result{Stdout: []byte(out), ExitCode: code}, err
		}, sys)
		return method(t, ms, "disk.smart").Handle(context.Background(), Caller{}, json.RawMessage(`{"device":"sda"}`))
	}
	got, err := read(`{"smart_status":{"passed":false}}`, 8|32, errExit)
	if err != nil || !got.(SmartReport).Failing {
		t.Fatalf("a failing disk: %+v %v", got, err)
	}
	got, err = read(ataOK, 64, errExit)
	if err != nil || got.(SmartReport).Failing || !got.(SmartReport).Supported {
		t.Fatalf("old errors in the log: %+v %v", got, err)
	}
	got, err = read(`{"smartctl":{"messages":[{"string":"Device is in STANDBY mode, exit(2)"}]}}`, 2, errExit)
	if err != nil || !got.(SmartReport).Asleep {
		t.Fatalf("a sleeping disk: %+v %v", got, err)
	}
	if _, err = read(`{"smartctl":{"messages":[{"string":"open failed"}]}}`, 2, errExit); err == nil || !strings.Contains(err.Error(), "open failed") {
		t.Fatalf("a real failure: %v", err)
	}
	if _, err = read("", -1, context.DeadlineExceeded); err == nil { // killed: no exit status
		t.Fatal("a timeout read as a report")
	}
}

func FuzzParseSmart(f *testing.F) {
	for _, s := range []string{ataOK, nvmeOK, `{}`, `null`, `[]`} {
		f.Add(s, 0)
	}
	f.Fuzz(func(t *testing.T, s string, code int) {
		r, err := parseSmart("sda", safeexec.Result{Stdout: []byte(s), ExitCode: code})
		if err != nil {
			return
		}
		if r.LifePct != nil && (*r.LifePct < 0 || *r.LifePct > 100) {
			t.Fatalf("life %d", *r.LifePct)
		}
		if strings.ContainsFunc(r.Model, func(c rune) bool { return c < 32 || c == 127 }) {
			t.Fatalf("model %q", r.Model)
		}
	})
}
