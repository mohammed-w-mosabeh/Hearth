package collect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
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

// selectors returns the m= metric selectors of q (see store.MatchMetric).
func selectors(q url.Values) ([]string, error) {
	sel := q["m"]
	if len(sel) == 0 || len(sel) > maxLiveMetrics {
		return nil, errors.New("1 to 32 m= metric names required")
	}
	for _, m := range sel {
		if m == "" || len(m) > 200 {
			return nil, errors.New("bad metric name")
		}
	}
	return sel, nil
}

// HandleLive serves GET /api/v1/metrics/live?m=cpu.usage&m=net.rx:&since=<ms>.
// Each m is a metric name, or a prefix ending in ":" or "." that selects
// every metric under it (e.g. "net.rx:" for all interfaces). since (unix ms)
// skips older samples so a client can fill only what it lacks.
func (s *Sampler) HandleLive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sel, err := selectors(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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
	match := func(name string) bool { return store.MatchMetric(name, sel) }
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

// HistoryStore reads downsampled history (store.DB in production).
type HistoryStore interface {
	History(ctx context.Context, res store.Resolution, sel []string, from, to time.Time, step time.Duration) (store.History, error)
}

// historyRanges maps the UI's ranges to a table and bucket size, chosen so
// a chart gets at most ~1500 points. 10 minutes comes from the ring buffer
// (HandleLive) at full resolution.
var historyRanges = map[string]struct {
	span time.Duration
	res  store.Resolution
	step time.Duration
}{
	"24h": {24 * time.Hour, store.Res1m, time.Minute},
	"7d":  {7 * 24 * time.Hour, store.Res1m, 10 * time.Minute},
	"1y":  {365 * 24 * time.Hour, store.Res1h, 12 * time.Hour},
}

// HandleHistory returns GET /api/v1/metrics/history?range=24h|7d|1y&m=...
// The newest, still-open minute is not in the tables yet.
func HandleHistory(db HistoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rg, ok := historyRanges[q.Get("range")]
		if !ok {
			http.Error(w, "range must be 24h, 7d or 1y", http.StatusBadRequest)
			return
		}
		sel, err := selectors(q)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		now := time.Now()
		h, err := db.History(r.Context(), rg.res, sel, now.Add(-rg.span), now.Add(time.Minute), rg.step)
		if err != nil {
			http.Error(w, "history unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h)
	}
}
