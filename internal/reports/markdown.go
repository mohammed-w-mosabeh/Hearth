package reports

import (
	"fmt"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/posture"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// mdEscaper escapes what Markdown (CommonMark and GFM tables) would read
// as markup, so a system-derived string stays text: a container named
// "[x](javascript:…)" or "<img …>" is shown, not linked or rendered, and
// an escaped ":" stops GFM autolinking "http://…". Strings never start a
// line (cells, or after "# "), so list and heading markers need nothing.
var mdEscaper = func() *strings.Replacer {
	var pairs []string
	for _, c := range "\\`*_[]<>#!|~&:" {
		pairs = append(pairs, string(c), "\\"+string(c))
	}
	return strings.NewReplacer(pairs...)
}()

// md makes s safe as one Markdown table cell or heading.
func md(s string) string { return mdEscaper.Replace(oneLine(s)) }

type mdWriter struct{ b strings.Builder }

func (w *mdWriter) p(format string, a ...any) { fmt.Fprintf(&w.b, format, a...) }

func (w *mdWriter) h2(key string) { w.p("\n## %s\n\n", md(msg(key))) }

func (w *mdWriter) note(key string) { w.p("_%s_\n", md(msg(key))) }

// table writes a GFM table; head cells are message keys, rows are already
// escaped. A head key starting with ">" is a right-aligned column.
func (w *mdWriter) table(head []string, rows [][]string) {
	var sep []string
	for i, h := range head {
		if k, ok := strings.CutPrefix(h, ">"); ok {
			head[i], h = k, k
			sep = append(sep, "--:")
		} else {
			sep = append(sep, "---")
		}
		head[i] = md(msg(h))
	}
	w.p("| %s |\n| %s |\n", strings.Join(head, " | "), strings.Join(sep, " | "))
	for _, r := range rows {
		w.p("| %s |\n", strings.Join(r, " | "))
	}
}

func (w *mdWriter) events(evs []store.AlertEvent) {
	rows := make([][]string, 0, len(evs))
	for _, e := range evs {
		rows = append(rows, []string{fmtMs(e.StartedAt), md(e.RuleName), md(e.Instance), md(e.Severity), md(alertValue(e.Metric, e.Peak)), fmtEnded(e.EndedAt)})
	}
	w.table([]string{"started", "rule", "on", "severity", ">peak", "ended"}, rows)
}

func (w *mdWriter) changes(cs []store.AuditRow) {
	rows := make([][]string, 0, len(cs))
	for _, c := range cs {
		rows = append(rows, []string{fmtTime(c.Time), md(c.Action), md(c.Target), md(c.Result), md(c.Origin)})
	}
	w.table([]string{"time", "action", "target", "result", "origin"}, rows)
}

func (w *mdWriter) header(title, period, from, to, gen string) {
	w.p("# %s\n\n", title)
	w.p("%s: %s – %s · %s %s\n", md(periodLabel(period)), from, to, md(msg("generated")), gen)
}

// MachineMarkdown renders m as Markdown.
func MachineMarkdown(m Machine) []byte {
	var w mdWriter
	title := md(msg("title.machine"))
	if m.Host != "" {
		title += " — " + md(m.Host)
	}
	w.header(title, m.Period, fmtTime(m.From), fmtTime(m.To), fmtTime(m.Generated))
	if m.Covered < 0.9 {
		w.p("\n_%s_\n", md(fmt.Sprintf(msg("covered"), fmtCovered(m.Covered))))
	}

	w.h2("resources")
	if len(m.Stats) == 0 {
		w.note("no.data")
	} else {
		rows := [][]string{}
		for _, s := range m.Stats {
			total := ""
			if s.Total != nil {
				total = fmtBytes(*s.Total)
			}
			rows = append(rows, []string{md(s.Label), fmtValue(s.Avg, s.Unit), fmtValue(s.Max, s.Unit), total})
		}
		w.table([]string{"metric", ">avg", ">peak", ">total"}, rows)
	}

	w.h2("top")
	if len(m.Top) == 0 {
		w.note("top.none")
	} else {
		rows := [][]string{}
		for _, t := range m.Top {
			rows = append(rows, []string{md(t.Name), md(kindLabel(t.Kind)), fmtValue(t.CPUAvg, "%"), fmtValue(t.CPUMax, "%"),
				fmtCPUTime(t.Busy), fmtBytes(t.MemAvg), fmtBytes(t.MemMax)})
		}
		w.table([]string{"name", "kind", ">cpu.avg", ">cpu.peak", ">cpu.time", ">mem.avg", ">mem.peak"}, rows)
	}

	if len(m.Disks) > 0 {
		w.h2("disks")
		rows := [][]string{}
		for _, d := range m.Disks {
			g := ""
			if d.Growth != nil {
				g = fmtSigned(*d.Growth)
			}
			rows = append(rows, []string{md(d.Mount), fmtBytes(float64(d.Used)), fmtBytes(float64(d.Total)), fmtNum(d.Pct, 0) + " %", g})
		}
		w.table([]string{"mount", ">used", ">size", ">%", ">change"}, rows)
	}
	if len(m.Batteries) > 0 {
		w.h2("batteries")
		rows := [][]string{}
		for _, b := range m.Batteries {
			name := md(b.ID)
			if b.Model != "" {
				name += " · " + md(b.Model)
			}
			cycles := ""
			if b.Cycles > 0 {
				cycles = fmt.Sprint(b.Cycles)
			}
			rows = append(rows, []string{name, fmtNum(b.HealthPct, 0) + " %", cycles, fmtNum(b.Pct, 0) + " %"})
		}
		w.table([]string{"battery", ">health", ">cycles", ">charge"}, rows)
	}

	if m.Posture != nil {
		w.h2("posture")
		rows := [][]string{}
		for _, c := range m.Posture.Checks {
			var cells []string
			for _, t := range findingTexts(c) {
				cells = append(cells, md(t))
			}
			rows = append(rows, []string{md(posture.CheckName(c.ID)), md(statusLabel(c.Status)), strings.Join(cells, "; ")})
		}
		w.table([]string{"check", "status", "findings"}, rows)
	}

	w.h2("alerts")
	if len(m.Alerts) == 0 {
		w.note("alerts.none")
	} else {
		w.events(m.Alerts)
	}
	w.h2("changes")
	if len(m.Changes) == 0 {
		w.note("timeline.none")
	} else {
		rows := make([][]string, 0, len(m.Changes))
		for _, e := range m.Changes {
			rows = append(rows, []string{fmtTime(e.Time), md(eventWhat(e)), md(e.Subject), md(eventDetail(e))})
		}
		w.table([]string{"time", "what", "target", "detail"}, rows)
	}
	return []byte(w.b.String())
}

// ItemMarkdown renders it as Markdown.
func ItemMarkdown(it Item) []byte {
	var w mdWriter
	w.header(md(it.Name), it.Period, fmtTime(it.From), fmtTime(it.To), fmtTime(it.Generated))
	w.p("\n%s · %s\n", md(kindLabel(it.Kind)), md(it.ID))

	w.h2("summary")
	if it.Total == nil {
		w.note("item.none")
	} else {
		t := it.Total
		w.table([]string{"cpu.avg", "cpu.peak", "cpu.time", "mem.avg", "mem.peak", "hours"}, [][]string{{
			fmtValue(t.CPUAvg, "%"), fmtValue(t.CPUMax, "%"), fmtCPUTime(t.Busy), fmtBytes(t.MemAvg), fmtBytes(t.MemMax), fmt.Sprint(t.Hours)}})
	}
	if it.PeakCPU != nil && it.PeakMem != nil {
		w.h2("peaks")
		w.p("- %s: %s at %s\n- %s: %s at %s\n",
			md(msg("peak.cpu")), fmtValue(it.PeakCPU.CPUMax, "%"), fmtTime(it.PeakCPU.Time),
			md(msg("peak.mem")), fmtBytes(it.PeakMem.MemMax), fmtTime(it.PeakMem.Time))
	}
	w.h2("events")
	if len(it.Events) == 0 {
		w.note("events.none")
	} else {
		w.events(it.Events)
	}
	w.h2("item.changes")
	if len(it.Changes) == 0 {
		w.note("changes.none")
	} else {
		w.changes(it.Changes)
	}
	if len(it.Hours) > 0 {
		w.h2("item.hours")
		rows := [][]string{}
		for _, h := range it.Hours {
			rows = append(rows, []string{fmtTime(h.Time), fmtValue(h.CPUAvg, "%"), fmtValue(h.CPUMax, "%"), fmtBytes(h.MemAvg), fmtBytes(h.MemMax)})
		}
		w.table([]string{"hour", ">cpu.avg", ">cpu.peak", ">mem.avg", ">mem.peak"}, rows)
	}
	return []byte(w.b.String())
}
