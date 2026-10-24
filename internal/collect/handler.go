package collect

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxLiveMetrics bounds how many metric selectors one request may name.
const maxLiveMetrics = 32

// Live is recent history from the ring buffer as columns: T holds unix
// milliseconds and each series has one value per T (null where the sample
// lacked that metric).
type Live struct {
	T      []int64               `json:"t"`
	Series map[string][]*float64 `json:"series"`
}

// HandleLive serves GET /api/v1/metrics/live?m=cpu.usage&m=net.rx:&since=<ms>.
// Each m is a metric name, or a prefix ending in ":" that selects every
// metric under it (e.g. "net.rx:" for all interfaces). since (unix ms) skips
// older samples so a client can fill only what it lacks.
func (s *Sampler) HandleLive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sel := q["m"]
	if len(sel) == 0 || len(sel) > maxLiveMetrics {
		http.Error(w, "1 to 32 m= metric names required", http.StatusBadRequest)
		return
	}
	for _, m := range sel {
		if m == "" || len(m) > 200 {
			http.Error(w, "bad metric name", http.StatusBadRequest)
			return
		}
	}
	var since time.Time
	if v := q.Get("since"); v != "" {
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "since must be unix milliseconds", http.StatusBadRequest)
			return
		}
		since = time.UnixMilli(ms)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(liveSeries(s.cfg.Ring.Snapshot(), sel, since))
}

func liveSeries(samples []Sample, sel []string, since time.Time) Live {
	match := func(name string) bool {
		for _, m := range sel {
			if name == m || (strings.HasSuffix(m, ":") && strings.HasPrefix(name, m)) {
				return true
			}
		}
		return false
	}
	out := Live{T: []int64{}, Series: map[string][]*float64{}}
	for _, smp := range samples {
		if !smp.Time.After(since) {
			continue
		}
		i := len(out.T)
		out.T = append(out.T, smp.Time.UnixMilli())
		for _, p := range smp.Points() {
			if !match(p.Metric) {
				continue
			}
			col, ok := out.Series[p.Metric]
			if !ok {
				col = make([]*float64, i, i+1) // nil for earlier samples
			}
			v := p.Value
			out.Series[p.Metric] = append(col, &v)
		}
		for name, col := range out.Series { // pad series this sample lacked
			if len(col) == i {
				out.Series[name] = append(col, nil)
			}
		}
	}
	return out
}
