package collect

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// OpenMetrics renders the latest sample and the number of open alerts as an
// OpenMetrics text exposition. It reads what the sampler already holds, so a
// scrape adds no sampling. Mount, interface and battery names are system
// strings and travel only as escaped label values.
func OpenMetrics(s Sample, firing int) []byte {
	var b bytes.Buffer
	gauge := func(name, help string, value float64) {
		family(&b, name, "gauge", help)
		fmt.Fprintf(&b, "%s %s\n", name, gaugeValue(value))
	}
	gauge("hearth_alerts_firing", "Alerts open now.", float64(firing))
	if s.Time.IsZero() {
		b.WriteString("# EOF\n")
		return b.Bytes()
	}
	gauge("hearth_sample_timestamp_seconds", "When the sample was taken.", float64(s.Time.UnixMilli())/1000)
	if !s.Partial {
		gauge("hearth_cpu_usage_percent", "CPU in use.", s.CPU.Usage)
	}
	gauge("hearth_load1", "Load average over 1 minute.", s.CPU.Load1)
	gauge("hearth_load5", "Load average over 5 minutes.", s.CPU.Load5)
	gauge("hearth_load15", "Load average over 15 minutes.", s.CPU.Load15)
	gauge("hearth_memory_total_bytes", "Memory installed.", float64(s.Mem.Total))
	gauge("hearth_memory_used_bytes", "Memory in use, without what can be reclaimed.", float64(s.Mem.Used))
	gauge("hearth_swap_total_bytes", "Swap available.", float64(s.Mem.SwapTotal))
	gauge("hearth_swap_used_bytes", "Swap in use.", float64(s.Mem.SwapUsed))
	if s.CPUTempC != nil {
		gauge("hearth_cpu_temperature_celsius", "CPU package temperature.", *s.CPUTempC)
	}

	byMount := func(name, help string, value func(Disk) uint64) {
		if len(s.Disks) == 0 {
			return
		}
		family(&b, name, "gauge", help)
		for _, d := range s.Disks {
			fmt.Fprintf(&b, "%s{mount=\"%s\"} %d\n", name, escapeLabel(d.Mount), value(d))
		}
	}
	byMount("hearth_filesystem_size_bytes", "Filesystem size.", func(d Disk) uint64 { return d.Total })
	byMount("hearth_filesystem_used_bytes", "Filesystem space in use.", func(d Disk) uint64 { return d.Used })
	byMount("hearth_filesystem_avail_bytes", "Filesystem space free to users.", func(d Disk) uint64 { return d.Avail })

	byIf := func(name, help string, value func(NetIf) uint64) {
		first := true
		for _, n := range s.Net {
			if n.Virtual {
				continue
			}
			if first {
				family(&b, name, "counter", help)
				first = false
			}
			fmt.Fprintf(&b, "%s_total{interface=\"%s\"} %d\n", name, escapeLabel(n.Name), value(n))
		}
	}
	byIf("hearth_network_receive_bytes", "Bytes received, the kernel's counter.", func(n NetIf) uint64 { return n.RxBytes })
	byIf("hearth_network_transmit_bytes", "Bytes sent, the kernel's counter.", func(n NetIf) uint64 { return n.TxBytes })

	if len(s.Power.Batteries) > 0 {
		family(&b, "hearth_battery_percent", "gauge", "Battery charge.")
		for _, bat := range s.Power.Batteries {
			fmt.Fprintf(&b, "hearth_battery_percent{battery=\"%s\"} %s\n", escapeLabel(bat.ID), gaugeValue(bat.Pct))
		}
	}
	b.WriteString("# EOF\n")
	return b.Bytes()
}

func family(b *bytes.Buffer, name, kind, help string) {
	fmt.Fprintf(b, "# TYPE %s %s\n# HELP %s %s\n", name, kind, name, help)
}

func gaugeValue(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// labelEscaper is the text format's escaping: nothing in a value can end the
// quoted string or the line.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(strings.ToValidUTF8(s, "�")) }
