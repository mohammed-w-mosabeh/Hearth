package bootinfo

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const timeOut = "Startup finished in 3.2s (firmware) + 1.5s (loader) + 38.578s (kernel) + 2min 33.755s (userspace) = 3min 17.033s \ngraphical.target reached after 31.589s in userspace.\n"

const blameOut = `2min 6.668s fwupd-refresh.service
    26.127s vboxdrv.service
      189ms power-profiles-daemon.service
  1h 2.5s slow.mount
      5.958s systemd-fsck@dev-disk-by\x2duuid-f660.service
garbage
`

const chainOut = `The time when unit became active or started is printed after the "@" character.
The time the unit took to start is printed after the "+" character.

graphical.target @31.589s
└─power-profiles-daemon.service @31.399s +189ms
  └─multi-user.target @31.394s
    └─vboxdrv.service @5.203s +26.127s
      └─dev-disk-by\x2duuid-f660.device @1.635s
`

func TestSpan(t *testing.T) {
	for s, want := range map[string]int64{"189ms": 189, "26.127s": 26127, "2min 6.668s": 126668, "1h 2.5s": 3602500, "": 0, "812us": 1} {
		if got := span(s); got != want {
			t.Errorf("%q: %d", s, got)
		}
	}
}

func TestParseTime(t *testing.T) {
	var r Report
	parseTime(timeOut, &r)
	want := []Phase{{"firmware", 3200}, {"loader", 1500}, {"kernel", 38578}, {"userspace", 153755}}
	if !r.Finished || r.TotalMs != 197033 || len(r.Phases) != 4 || r.Target != "graphical.target" || r.TargetMs != 31589 {
		t.Fatalf("%+v", r)
	}
	for i := range want {
		if r.Phases[i] != want[i] {
			t.Errorf("%+v", r.Phases)
		}
	}
	var none Report
	parseTime("Bootup is not yet finished (org.freedesktop.systemd1.Manager.FinishTimestampMonotonic=0).\n", &none)
	if none.Finished {
		t.Error("not finished")
	}
}

func TestParseBlameAndChain(t *testing.T) {
	b := parseBlame(blameOut)
	if len(b) != 5 || b[0].Name != "slow.mount" || b[0].Ms != 3602500 || b[1].Name != "fwupd-refresh.service" || b[1].Ms != 126668 {
		t.Fatalf("%+v", b)
	}
	c := parseChain(chainOut)
	if len(c) != 5 || c[0].Name != "graphical.target" || c[0].AtMs != 31589 || c[0].Ms != 0 {
		t.Fatalf("%+v", c)
	}
	if c[3].Name != "vboxdrv.service" || c[3].AtMs != 5203 || c[3].Ms != 26127 || c[4].Name != `dev-disk-by\x2duuid-f660.device` {
		t.Fatalf("%+v", c)
	}
}

func TestRead(t *testing.T) {
	calls := []string{}
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		calls = append(calls, name)
		if err := safeexec.Check(name, args...); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		switch name {
		case "analyze-time":
			return safeexec.Result{Stdout: []byte(timeOut)}, nil
		case "analyze-blame":
			return safeexec.Result{Stdout: []byte(blameOut)}, nil
		}
		return safeexec.Result{Stdout: []byte(chainOut)}, nil
	}
	if r := Read(context.Background(), run); !r.Finished || len(r.Blame) != 5 || len(r.Chain) != 5 || len(calls) != 3 {
		t.Fatalf("%+v %v", r, calls)
	}
	calls = nil
	unfinished := func(_ context.Context, name string, _ ...string) (safeexec.Result, error) {
		calls = append(calls, name)
		return safeexec.Result{Stdout: []byte("Bootup is not yet finished")}, nil
	}
	if r := Read(context.Background(), unfinished); r.Finished || len(calls) != 1 || r.Blame == nil {
		t.Fatalf("%+v %v", r, calls)
	}
	bad := func(context.Context, string, ...string) (safeexec.Result, error) {
		return safeexec.Result{}, errors.New("no")
	}
	rec := httptest.NewRecorder()
	Handle(bad)(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("%s", rec.Body)
	}
}
