package collect

import (
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/power"
)

func TestOpenMetrics(t *testing.T) {
	temp := 51.5
	out := string(OpenMetrics(Sample{
		Time:     time.UnixMilli(1_700_000_000_500),
		CPU:      CPU{Usage: 12.5, Load1: 1},
		Mem:      Mem{Total: 100, Used: 40},
		CPUTempC: &temp,
		Disks:    []Disk{{Mount: "/mnt/a\"b\\c\nd", Total: 10, Used: 4, Avail: 6}},
		Net:      []NetIf{{Name: "eth0", RxBytes: 7, TxBytes: 8}, {Name: "docker0", Virtual: true, RxBytes: 9}},
		Power:    power.State{Batteries: []power.Battery{{ID: "BAT0", Pct: 80}}},
	}, 2))
	for _, want := range []string{
		"# TYPE hearth_alerts_firing gauge\n",
		"hearth_alerts_firing 2\n",
		"hearth_sample_timestamp_seconds 1700000000.5\n",
		"hearth_cpu_usage_percent 12.5\n",
		"hearth_cpu_temperature_celsius 51.5\n",
		`hearth_filesystem_used_bytes{mount="/mnt/a\"b\\c\nd"} 4` + "\n",
		"# TYPE hearth_network_receive_bytes counter\n",
		`hearth_network_receive_bytes_total{interface="eth0"} 7` + "\n",
		`hearth_battery_percent{battery="BAT0"} 80` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "docker0") {
		t.Error("a virtual interface was exported")
	}
	if !strings.HasSuffix(out, "# EOF\n") {
		t.Errorf("does not end in # EOF: %q", out[len(out)-20:])
	}
	if strings.Count(out, "# EOF") != 1 {
		t.Error("more than one # EOF")
	}
}

func TestOpenMetricsLeavesOutWhatIsNotKnown(t *testing.T) {
	if got := string(OpenMetrics(Sample{}, 0)); got != "# TYPE hearth_alerts_firing gauge\n# HELP hearth_alerts_firing Alerts open now.\nhearth_alerts_firing 0\n# EOF\n" {
		t.Errorf("no sample: %q", got)
	}
	out := string(OpenMetrics(Sample{Time: time.Now(), Partial: true}, 0))
	if strings.Contains(out, "hearth_cpu_usage_percent") {
		t.Error("a partial sample reported CPU as zero")
	}
}

func FuzzEscapeLabel(f *testing.F) {
	for _, s := range []string{"", `a"b`, `a\`, "a\nb", "\xff", "# EOF", "}\nhearth_x 1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := escapeLabel(s)
		if strings.Contains(got, "\n") {
			t.Fatalf("newline survived: %q", got)
		}
		// Every quote is escaped: an odd run of backslashes precedes it.
		for i := range len(got) {
			if got[i] != '"' {
				continue
			}
			n := 0
			for j := i - 1; j >= 0 && got[j] == '\\'; j-- {
				n++
			}
			if n%2 == 0 {
				t.Fatalf("bare quote in %q", got)
			}
		}
		if (len(got)-len(strings.TrimRight(got, `\`)))%2 != 0 {
			t.Fatalf("value ends in a lone backslash: %q", got)
		}
	})
}
