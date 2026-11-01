package reports

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/posture"
)

// The exported page is one file: its stylesheet is inline and allowed by
// hash, it has no script, and nothing loads from anywhere (SECURITY S4
// holds for the file as for the UI). System-derived strings are escaped
// by html/template.
const reportCSS = `:root{--fg:#1d1b18;--muted:#6b645b;--line:#e4ded5;--bg:#fffdf9;--accent:#c47f17;--crit:#b3261e;--warn:#a15c00}
@media (prefers-color-scheme:dark){:root{--fg:#ece7df;--muted:#a49b8f;--line:#3a352f;--bg:#1b1916;--accent:#e9a23b;--crit:#f2b8b5;--warn:#f0c27a}}
*{box-sizing:border-box}
body{margin:0 auto;max-width:960px;padding:24px 16px;font:14px/1.5 system-ui,sans-serif;color:var(--fg);background:var(--bg)}
h1{font-size:22px;margin:0}h2{font-size:16px;margin:28px 0 8px;border-bottom:1px solid var(--line);padding-bottom:4px}
.sub{color:var(--muted);margin:4px 0 0}.note{color:var(--muted)}
table{width:100%;border-collapse:collapse;font-variant-numeric:tabular-nums}
th,td{text-align:left;padding:4px 8px;border-bottom:1px solid var(--line);vertical-align:top;overflow-wrap:anywhere}
th{color:var(--muted);font-weight:600}td.n,th.n{text-align:right;white-space:nowrap}td.t,td.k{white-space:nowrap}
.trends{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:16px}
figure{margin:0}figcaption{color:var(--muted);font-size:12px}
svg{width:100%;height:80px;display:block;border-bottom:1px solid var(--line)}
svg polyline{fill:none;stroke:var(--accent);stroke-width:1.5}
svg line{stroke:var(--line);stroke-dasharray:2 3}
.critical{color:var(--crit)}.warning{color:var(--warn)}
@media print{body{max-width:none;padding:0;color:#000;background:#fff}h2{break-after:avoid}tr,figure{break-inside:avoid}svg polyline{stroke:#000}}`

var cspStyle = func() string {
	sum := sha256.Sum256([]byte(reportCSS))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// CSP is the policy of an exported page.
var CSP = "default-src 'none'; style-src " + cspStyle + "; img-src data:; base-uri 'none'; form-action 'none'"

var funcs = template.FuncMap{
	"t":        msg,
	"kind":     kindLabel,
	"marks":    markLines,
	"bytes":    func(v any) string { return fmtBytes(toFloat(v)) },
	"signed":   fmtSigned,
	"num":      fmtNum,
	"value":    fmtValue,
	"time":     fmtTime,
	"ms":       fmtMs,
	"cputime":  fmtCPUTime,
	"period":   periodLabel,
	"alertval": alertValue,
	"ended":    fmtEnded,
	"covered":  fmtCovered,
	"line":     oneLine,
	"spark":    sparkline,
	"css":      func() template.CSS { return template.CSS(reportCSS) }, // #nosec G203 -- a constant stylesheet, no input
	"csp":      func() string { return CSP },
	"lowcover": func(c float64) bool { return c < 0.9 },
	"status":   statusLabel,
	"check":    posture.CheckName,
	"findings": findingTexts,
	"what":     eventWhat,
	"detail":   eventDetail,
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case *float64:
		if x != nil {
			return *x
		}
	case uint64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

// sparkline returns the polylines of a trend in a 600x80 box, one per
// run of recorded points; a lone point is a short dash its slot wide.
func sparkline(tr Trend) []string {
	const w, h = 600.0, 80.0
	var out []string
	var cur []string
	n := len(tr.V)
	for i, v := range tr.V {
		if v == nil {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, " "))
			}
			cur = nil
			continue
		}
		x := w * float64(i) / float64(max(n-1, 1))
		y := h - h*min(*v/tr.Max, 1)
		cur = append(cur, strconv.FormatFloat(x, 'f', 1, 64)+","+strconv.FormatFloat(y, 'f', 1, 64))
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	half := w / float64(max(n-1, 1)) / 2
	for i, p := range out {
		if !strings.Contains(p, " ") {
			xs, ys, _ := strings.Cut(p, ",")
			x, _ := strconv.ParseFloat(xs, 64)
			out[i] = strconv.FormatFloat(max(x-half, 0), 'f', 1, 64) + "," + ys + " " + strconv.FormatFloat(min(x+half, w), 'f', 1, 64) + "," + ys
		}
	}
	return out
}

// markLines returns the x of each event in a trend's 600-wide box, in step
// with sparkline: the first point is at 0, the last at 600.
func markLines(tr Trend) []string {
	if len(tr.T) < 2 {
		return nil
	}
	first, last := tr.T[0], tr.T[len(tr.T)-1]
	var out []string
	for _, ms := range tr.Marks {
		if ms >= first && ms <= last {
			out = append(out, strconv.FormatFloat(600*float64(ms-first)/float64(last-first), 'f', 1, 64))
		}
	}
	return out
}

const pageHead = `{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="{{csp}}">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>{{.}}</title>
<style>{{css}}</style></head><body>{{end}}`

const trendsTmpl = `{{define "trends"}}{{if .}}<div class="trends">{{range .}}<figure>
<svg viewBox="0 0 600 80" preserveAspectRatio="none" role="img" aria-label="{{.Label}}">{{range marks .}}<line x1="{{.}}" y1="0" x2="{{.}}" y2="80"/>{{end}}{{range spark .}}<polyline points="{{.}}"/>{{end}}</svg>
<figcaption>{{.Label}} · {{t "peak"}} {{value .Peak .Unit}}</figcaption></figure>{{end}}</div>{{end}}{{end}}`

const changesTmpl = `{{define "changes"}}<table><thead><tr><th>{{t "time"}}</th><th>{{t "action"}}</th><th>{{t "target"}}</th><th>{{t "result"}}</th><th>{{t "origin"}}</th></tr></thead><tbody>
{{range .}}<tr><td class="t">{{time .Time}}</td><td>{{line .Action}}</td><td>{{line .Target}}</td><td>{{line .Result}}</td><td>{{.Origin}}</td></tr>
{{end}}</tbody></table>{{end}}`

const timelineTmpl = `{{define "timeline"}}<table><thead><tr><th>{{t "time"}}</th><th>{{t "what"}}</th><th>{{t "target"}}</th><th>{{t "detail"}}</th></tr></thead><tbody>
{{range .}}<tr><td class="t">{{time .Time}}</td><td>{{line (what .)}}</td><td>{{line .Subject}}</td><td>{{line (detail .)}}</td></tr>
{{end}}</tbody></table>{{end}}`

const eventsTmpl = `{{define "events"}}<table><thead><tr><th>{{t "started"}}</th><th>{{t "rule"}}</th><th>{{t "on"}}</th><th>{{t "severity"}}</th><th class="n">{{t "peak"}}</th><th>{{t "ended"}}</th></tr></thead><tbody>
{{range .}}<tr><td class="t">{{ms .StartedAt}}</td><td>{{line .RuleName}}</td><td>{{line .Instance}}</td><td class="{{.Severity}}">{{.Severity}}</td><td class="n">{{alertval .Metric .Peak}}</td><td class="t">{{ended .EndedAt}}</td></tr>
{{end}}</tbody></table>{{end}}`

const machineTmpl = `{{template "head" (printf "%s — %s" (t "title.machine") (line .Host))}}
<header><h1>{{t "title.machine"}}{{if .Host}} — {{line .Host}}{{end}}</h1>
<p class="sub">{{period .Period}}: {{time .From}} – {{time .To}} · {{t "generated"}} {{time .Generated}}</p></header>
{{if lowcover .Covered}}<p class="note">{{printf (t "covered") (covered .Covered)}}</p>{{end}}

<h2>{{t "resources"}}</h2>
{{if .Stats}}<table><thead><tr><th>{{t "metric"}}</th><th class="n">{{t "avg"}}</th><th class="n">{{t "peak"}}</th><th class="n">{{t "total"}}</th></tr></thead><tbody>
{{range .Stats}}<tr><td>{{line .Label}}</td><td class="n">{{value .Avg .Unit}}</td><td class="n">{{value .Max .Unit}}</td><td class="n">{{if .Total}}{{bytes .Total}}{{end}}</td></tr>
{{end}}</tbody></table>
<h2>{{t "trends"}}</h2>{{template "trends" .Trends}}{{else}}<p class="note">{{t "no.data"}}</p>{{end}}

<h2>{{t "top"}}</h2>
{{if .Top}}<table><thead><tr><th>{{t "name"}}</th><th>{{t "kind"}}</th><th class="n">{{t "cpu.avg"}}</th><th class="n">{{t "cpu.peak"}}</th><th class="n">{{t "cpu.time"}}</th><th class="n">{{t "mem.avg"}}</th><th class="n">{{t "mem.peak"}}</th></tr></thead><tbody>
{{range .Top}}<tr><td>{{line .Name}}</td><td class="k">{{kind .Kind}}</td><td class="n">{{value .CPUAvg "%"}}</td><td class="n">{{value .CPUMax "%"}}</td><td class="n">{{cputime .Busy}}</td><td class="n">{{bytes .MemAvg}}</td><td class="n">{{bytes .MemMax}}</td></tr>
{{end}}</tbody></table>{{else}}<p class="note">{{t "top.none"}}</p>{{end}}

{{if .Disks}}<h2>{{t "disks"}}</h2>
<table><thead><tr><th>{{t "mount"}}</th><th class="n">{{t "used"}}</th><th class="n">{{t "size"}}</th><th class="n">%</th><th class="n">{{t "change"}}</th></tr></thead><tbody>
{{range .Disks}}<tr><td>{{line .Mount}}</td><td class="n">{{bytes .Used}}</td><td class="n">{{bytes .Total}}</td><td class="n">{{num .Pct 0}} %</td><td class="n">{{if .Growth}}{{signed .Growth}}{{end}}</td></tr>
{{end}}</tbody></table>{{end}}

{{if .Batteries}}<h2>{{t "batteries"}}</h2>
<table><thead><tr><th>{{t "battery"}}</th><th class="n">{{t "health"}}</th><th class="n">{{t "cycles"}}</th><th class="n">{{t "charge"}}</th></tr></thead><tbody>
{{range .Batteries}}<tr><td>{{line .ID}}{{if .Model}} · {{line .Model}}{{end}}</td><td class="n">{{num .HealthPct 0}} %</td><td class="n">{{if .Cycles}}{{.Cycles}}{{end}}</td><td class="n">{{num .Pct 0}} %</td></tr>
{{end}}</tbody></table>{{end}}

{{with .Posture}}<h2>{{t "posture"}}</h2>
<table><thead><tr><th>{{t "check"}}</th><th>{{t "status"}}</th><th>{{t "findings"}}</th></tr></thead><tbody>
{{range .Checks}}<tr><td>{{check .ID}}</td><td class="k {{.Status}}">{{status .Status}}</td><td>{{range $i, $f := findings .}}{{if $i}}<br>{{end}}{{line $f}}{{end}}</td></tr>
{{end}}</tbody></table>{{end}}

<h2>{{t "alerts"}}</h2>
{{if .Alerts}}{{template "events" .Alerts}}{{else}}<p class="note">{{t "alerts.none"}}</p>{{end}}

<h2>{{t "changes"}}</h2>
{{if .Changes}}{{template "timeline" .Changes}}{{else}}<p class="note">{{t "timeline.none"}}</p>{{end}}
</body></html>
`

const itemTmpl = `{{template "head" (printf "%s — %s" (t "title.item") (line .Name))}}
<header><h1>{{line .Name}}</h1>
<p class="sub">{{kind .Kind}} · {{line .ID}}{{if .Host}} · {{line .Host}}{{end}}</p>
<p class="sub">{{period .Period}}: {{time .From}} – {{time .To}} · {{t "generated"}} {{time .Generated}}</p></header>

<h2>{{t "summary"}}</h2>
{{with .Total}}<table><tbody>
<tr><th>{{t "cpu.avg"}}</th><td class="n">{{value .CPUAvg "%"}}</td><th>{{t "cpu.time"}}</th><td class="n">{{cputime .Busy}}</td></tr>
<tr><th>{{t "mem.avg"}}</th><td class="n">{{bytes .MemAvg}}</td><th>{{t "hours"}}</th><td class="n">{{.Hours}}</td></tr>
</tbody></table>{{else}}<p class="note">{{t "item.none"}}</p>{{end}}
{{if .PeakCPU}}<h2>{{t "peaks"}}</h2><table><tbody>
<tr><th>{{t "peak.cpu"}}</th><td class="n">{{value .PeakCPU.CPUMax "%"}}</td><td class="n">{{time .PeakCPU.Time}}</td></tr>
<tr><th>{{t "peak.mem"}}</th><td class="n">{{bytes .PeakMem.MemMax}}</td><td class="n">{{time .PeakMem.Time}}</td></tr>
</tbody></table>
<h2>{{t "trends"}}</h2>{{template "trends" .Trends}}{{end}}

<h2>{{t "events"}}</h2>
{{if .Events}}{{template "events" .Events}}{{else}}<p class="note">{{t "events.none"}}</p>{{end}}

<h2>{{t "item.changes"}}</h2>
{{if .Changes}}{{template "changes" .Changes}}{{else}}<p class="note">{{t "changes.none"}}</p>{{end}}

{{if .Hours}}<h2>{{t "item.hours"}}</h2>
<table><thead><tr><th>{{t "hour"}}</th><th class="n">{{t "cpu.avg"}}</th><th class="n">{{t "cpu.peak"}}</th><th class="n">{{t "mem.avg"}}</th><th class="n">{{t "mem.peak"}}</th></tr></thead><tbody>
{{range .Hours}}<tr><td class="t">{{time .Time}}</td><td class="n">{{value .CPUAvg "%"}}</td><td class="n">{{value .CPUMax "%"}}</td><td class="n">{{bytes .MemAvg}}</td><td class="n">{{bytes .MemMax}}</td></tr>
{{end}}</tbody></table>{{end}}
</body></html>
`

var (
	machineHTML = template.Must(template.New("machine").Funcs(funcs).Parse(pageHead + trendsTmpl + changesTmpl + timelineTmpl + eventsTmpl + machineTmpl))
	itemHTML    = template.Must(template.New("item").Funcs(funcs).Parse(pageHead + trendsTmpl + changesTmpl + eventsTmpl + itemTmpl))
)

// MachineHTML renders m as a self-contained page.
func MachineHTML(m Machine) ([]byte, error) { return execute(machineHTML, m) }

// ItemHTML renders it as a self-contained page.
func ItemHTML(it Item) ([]byte, error) { return execute(itemHTML, it) }

func execute(t *template.Template, v any) ([]byte, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, v); err != nil {
		return nil, fmt.Errorf("reports: render: %w", err)
	}
	return b.Bytes(), nil
}
