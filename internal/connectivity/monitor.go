package connectivity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const (
	// SettingKey holds the Config.
	SettingKey = "connectivity"
	// Alert catalog ids.
	MetricDown = "net.internet_down"
	MetricCap  = "net.data_cap"
	// latencyPrefix is the history's metric, then the check's name.
	latencyPrefix = "conn.latency:"

	every = time.Minute
	// A longer gap between checks means the machine slept: what was
	// counted before it says nothing about now.
	sleptAfter = 3 * every
	// downAfter failed rounds make an outage, so one lost check does not.
	downAfter  = 2
	flushEvery = 5 * time.Minute
	keepFor    = 400 * 24 * time.Hour
	maxOutages = 50
	gib        = float64(1 << 30)
)

// Check kinds, and what an outage is put down to.
const (
	KindGateway = "gateway"
	KindDNS     = "dns"
	KindTarget  = "target"
	CauseLocal  = "gateway"  // the machine's own network
	CauseDNS    = "dns"      // names do not resolve
	CauseUp     = "upstream" // the gateway answers, nothing beyond does
)

// Result is one check.
type Result struct {
	Name string  `json:"name"`
	Kind string  `json:"kind"`
	OK   bool    `json:"ok"`
	Ms   float64 `json:"ms,omitempty"`
	// Skipped: it could not be tried here (ping not allowed, IPv6 gateway).
	Skipped bool `json:"skipped,omitempty"`
}

// Store is the subset of store.DB used here.
type Store interface {
	Setting(ctx context.Context, key string) (json.RawMessage, bool, error)
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
	AddNetUsage(ctx context.Context, rows []store.NetUsage) error
	NetUsageSince(ctx context.Context, day string) ([]store.NetUsage, error)
	OpenOutage(ctx context.Context, at time.Time, cause string) (int64, error)
	CloseOutages(ctx context.Context, at time.Time) error
	Outages(ctx context.Context, limit int) ([]store.Outage, error)
	PruneConnectivity(ctx context.Context, before time.Time) error
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Probes are the checks; tests replace them.
type Probes struct {
	TCP  func(ctx context.Context, host string, port int) (time.Duration, error)
	DNS  func(ctx context.Context, name string) (time.Duration, error)
	Ping func(ctx context.Context, host string) (time.Duration, error)
}

// Options configures a Monitor.
type Options struct {
	Store    Store
	Probes   Probes
	Gateways func() []string
	Observe  func(ctx context.Context, now time.Time, metric string, values map[string]float64)
	Now      func() time.Time
	Log      *slog.Logger
}

type counters struct{ rx, tx uint64 }

// Monitor runs the checks and counts bytes.
type Monitor struct {
	o Options

	mu      sync.Mutex
	cfg     Config
	results []Result
	failed  int
	down    bool
	last    time.Time

	base    map[string]counters // the kernel's counters at the last reading
	pending map[[2]string]*store.NetUsage
}

// New returns a Monitor; Load reads the saved config.
func New(o Options) *Monitor {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Probes.TCP == nil {
		o.Probes = Probes{TCP: TCPCheck, DNS: DNSCheck, Ping: PingCheck}
	}
	return &Monitor{o: o, cfg: Config{Targets: []Target{}}, results: []Result{}, base: map[string]counters{}, pending: map[[2]string]*store.NetUsage{}}
}

// Load reads the saved config. One that no longer validates is ignored.
func (m *Monitor) Load(ctx context.Context) {
	raw, ok, err := m.o.Store.Setting(ctx, SettingKey)
	if err != nil || !ok {
		return
	}
	var c Config
	if json.Unmarshal(raw, &c) != nil || c.Validate() != nil {
		return
	}
	if c.Targets == nil {
		c.Targets = []Target{}
	}
	m.mu.Lock()
	m.cfg = c
	m.mu.Unlock()
}

// Count adds what the kernel's counters moved since the last reading. A
// counter that went back was reset (a reboot, the interface came back):
// the reading is a new start, nothing is added.
func (m *Monitor) Count(at time.Time, iface string, rx, tx uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, seen := m.base[iface]
	m.base[iface] = counters{rx, tx}
	if !seen || rx < prev.rx || tx < prev.tx {
		return
	}
	day := at.Local().Format(time.DateOnly)
	k := [2]string{iface, day}
	u := m.pending[k]
	if u == nil {
		u = &store.NetUsage{Iface: iface, Day: day}
		m.pending[k] = u
	}
	u.Rx += int64(min(rx-prev.rx, 1<<62)) // #nosec G115 -- bounded on this line
	u.Tx += int64(min(tx-prev.tx, 1<<62)) // #nosec G115 -- bounded on this line
}

// Flush stores what was counted. On failure it stays for the next time.
func (m *Monitor) Flush(ctx context.Context) {
	m.mu.Lock()
	rows := make([]store.NetUsage, 0, len(m.pending))
	for _, u := range m.pending {
		rows = append(rows, *u)
	}
	m.pending = map[[2]string]*store.NetUsage{}
	m.mu.Unlock()
	if err := m.o.Store.AddNetUsage(ctx, rows); err != nil {
		m.o.Log.Warn("connectivity: usage", "err", err)
		m.mu.Lock()
		for _, r := range rows {
			k := [2]string{r.Iface, r.Day}
			if u := m.pending[k]; u != nil {
				u.Rx += r.Rx
				u.Tx += r.Tx
			} else {
				c := r
				m.pending[k] = &c
			}
		}
		m.mu.Unlock()
	}
}

// Points are the last latencies, for the metric history.
func (m *Monitor) Points() []store.Point {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Point
	for _, r := range m.results {
		if r.OK {
			out = append(out, store.Point{Metric: latencyPrefix + r.Name, Value: r.Ms})
		}
	}
	return out
}

func (m *Monitor) checkTarget(ctx context.Context, t Target) Result {
	r := Result{Name: t.label(), Kind: KindTarget}
	var took time.Duration
	var err error
	if t.Port == 0 {
		took, err = m.o.Probes.Ping(ctx, t.Host)
	} else {
		took, err = m.o.Probes.TCP(ctx, t.Host, t.Port)
	}
	r.OK, r.Ms = err == nil, float64(took.Microseconds())/1000
	r.Skipped = errors.Is(err, ErrNoPing)
	return r
}

// round runs every check at once.
func (m *Monitor) round(ctx context.Context, c Config) []Result {
	out := make([]Result, len(c.Targets))
	var wg sync.WaitGroup
	for i, t := range c.Targets {
		wg.Add(1)
		go func() { defer wg.Done(); out[i] = m.checkTarget(ctx, t) }()
	}
	gw := Result{Name: "gateway", Kind: KindGateway}
	gates := m.o.Gateways()
	wg.Add(1)
	go func() {
		defer wg.Done()
		gw.Skipped = len(gates) > 0
		for _, a := range gates {
			if !isV4(a) {
				continue
			}
			took, err := m.o.Probes.Ping(ctx, a)
			gw.OK, gw.Ms, gw.Skipped = err == nil, float64(took.Microseconds())/1000, errors.Is(err, ErrNoPing)
			return
		}
	}()
	dns := Result{Name: "dns", Kind: KindDNS, Skipped: c.DNSName == ""}
	if c.DNSName != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			took, err := m.o.Probes.DNS(ctx, c.DNSName)
			dns.OK, dns.Ms = err == nil, float64(took.Microseconds())/1000
		}()
	}
	wg.Wait()
	return append([]Result{gw, dns}, out...)
}

// judge says whether the round reached anything, and if not, why not.
func judge(rs []Result) (up, known bool, cause string) {
	cause = CauseUp
	for _, r := range rs {
		switch {
		case r.Skipped:
		case r.Kind == KindTarget && r.OK:
			return true, true, ""
		case r.Kind == KindTarget:
			known = true
		case r.Kind == KindDNS && !r.OK && cause == CauseUp:
			cause = CauseDNS
		case r.Kind == KindGateway && !r.OK:
			cause = CauseLocal
		}
	}
	return false, known, cause
}

// apply records a round; it is the only place an outage starts or ends.
func (m *Monitor) apply(ctx context.Context, now time.Time, rs []Result) {
	up, known, cause := judge(rs)
	m.mu.Lock()
	if !m.last.IsZero() && now.Sub(m.last) > sleptAfter {
		m.failed = 0
	}
	m.last = now
	m.results = rs
	start, end := false, false
	switch {
	case up:
		m.failed = 0
		end, m.down = m.down, false
	case known:
		m.failed++
		if m.failed >= downAfter && !m.down {
			m.down, start = true, true
		}
	}
	down := m.down
	m.mu.Unlock()
	if start {
		if _, err := m.o.Store.OpenOutage(ctx, now, cause); err != nil {
			m.o.Log.Warn("connectivity: outage", "err", err)
		}
	}
	if end {
		if err := m.o.Store.CloseOutages(ctx, now); err != nil {
			m.o.Log.Warn("connectivity: outage", "err", err)
		}
	}
	if m.o.Observe != nil {
		v := 0.0
		if down {
			v = 1
		}
		m.o.Observe(ctx, now, MetricDown, map[string]float64{"": v})
	}
}

// monthStart is the first day of now's month, local.
func monthStart(now time.Time) string {
	return now.Local().Format("2006-01") + "-01"
}

// checkCap feeds the data cap alert.
func (m *Monitor) checkCap(ctx context.Context, now time.Time, c Config) {
	if m.o.Observe == nil {
		return
	}
	values := map[string]float64{}
	if c.CapGiB > 0 {
		rows, err := m.o.Store.NetUsageSince(ctx, monthStart(now))
		if err != nil {
			return
		}
		var total float64
		for _, r := range rows {
			if r.Iface == c.CapIface {
				total += float64(r.Rx) + float64(r.Tx)
			}
		}
		values[c.CapIface] = 0
		if total/gib >= c.CapGiB {
			values[c.CapIface] = 1
		}
	}
	m.o.Observe(ctx, now, MetricCap, values)
}

func (m *Monitor) tick(ctx context.Context) {
	now := m.o.Now()
	m.mu.Lock()
	c := m.cfg
	m.mu.Unlock()
	if c.Enabled {
		m.apply(ctx, now, m.round(ctx, c))
	}
}

// Run checks every minute, while enabled, and stores the byte counts.
func (m *Monitor) Run(ctx context.Context) {
	m.Load(ctx)
	// An outage left open by a restart ended somewhere in between.
	if err := m.o.Store.CloseOutages(ctx, m.o.Now()); err != nil {
		m.o.Log.Warn("connectivity: outage", "err", err)
	}
	checks := time.NewTicker(every)
	flush := time.NewTicker(flushEvery)
	defer checks.Stop()
	defer flush.Stop()
	for {
		select {
		case <-ctx.Done():
			m.Flush(context.WithoutCancel(ctx))
			return
		case <-checks.C:
			m.tick(ctx)
		case <-flush.C:
			m.Flush(ctx)
			now := m.o.Now()
			m.mu.Lock()
			c := m.cfg
			m.mu.Unlock()
			m.checkCap(ctx, now, c)
			if err := m.o.Store.PruneConnectivity(ctx, now.Add(-keepFor)); err != nil {
				m.o.Log.Warn("connectivity: prune", "err", err)
			}
		}
	}
}

// IfaceUsage is one interface's traffic.
type IfaceUsage struct {
	Iface   string           `json:"iface"`
	TodayRx int64            `json:"today_rx"`
	TodayTx int64            `json:"today_tx"`
	MonthRx int64            `json:"month_rx"`
	MonthTx int64            `json:"month_tx"`
	Days    []store.NetUsage `json:"days"`
}

// summarize folds a month's rows by interface.
func summarize(rows []store.NetUsage, today string) []IfaceUsage {
	by := map[string]*IfaceUsage{}
	for _, r := range rows {
		u := by[r.Iface]
		if u == nil {
			u = &IfaceUsage{Iface: r.Iface, Days: []store.NetUsage{}}
			by[r.Iface] = u
		}
		u.MonthRx += r.Rx
		u.MonthTx += r.Tx
		if r.Day == today {
			u.TodayRx, u.TodayTx = r.Rx, r.Tx
		}
		u.Days = append(u.Days, r)
	}
	out := make([]IfaceUsage, 0, len(by))
	for _, u := range by {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Iface < out[j].Iface })
	return out
}

// Report is GET /api/v1/connectivity.
type Report struct {
	Config  Config         `json:"config"`
	Checks  []Result       `json:"checks"`
	Down    bool           `json:"down"`
	Outages []store.Outage `json:"outages"`
	Usage   []IfaceUsage   `json:"usage"`
}

// Read builds the report.
func (m *Monitor) Read(ctx context.Context) (Report, error) {
	m.Flush(ctx)
	now := m.o.Now()
	m.mu.Lock()
	r := Report{Config: m.cfg, Checks: m.results, Down: m.down}
	m.mu.Unlock()
	var err error
	if r.Outages, err = m.o.Store.Outages(ctx, maxOutages); err != nil {
		return r, err
	}
	rows, err := m.o.Store.NetUsageSince(ctx, monthStart(now))
	if err != nil {
		return r, err
	}
	r.Usage = summarize(rows, now.Local().Format(time.DateOnly))
	return r, nil
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *Monitor) handleGet(w http.ResponseWriter, r *http.Request) {
	rep, err := m.Read(r.Context())
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "connectivity could not be read"})
		return
	}
	reply(w, http.StatusOK, rep)
}

// handleSave serves PUT /api/v1/connectivity/config.
func (m *Monitor) handleSave(w http.ResponseWriter, r *http.Request) {
	var c Config
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "bad config"})
		return
	}
	if err := c.Validate(); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if c.Targets == nil {
		c.Targets = []Target{}
	}
	raw, err := json.Marshal(c)
	if err == nil {
		err = m.o.Store.SetSetting(r.Context(), SettingKey, raw)
	}
	result := "ok"
	if err != nil {
		result = "error: not saved"
	}
	target := "off"
	if c.Enabled {
		target = "on"
	}
	if aerr := m.o.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, Action: "connectivity.configure", Target: target, Result: result}); aerr != nil {
		m.o.Log.Warn("connectivity: audit", "err", aerr)
	}
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "the settings could not be saved"})
		return
	}
	m.mu.Lock()
	m.cfg = c
	if !c.Enabled {
		m.results, m.failed = []Result{}, 0
	}
	wasDown := m.down && !c.Enabled
	if wasDown {
		m.down = false
	}
	m.mu.Unlock()
	now := m.o.Now()
	if !c.Enabled && m.o.Observe != nil {
		m.o.Observe(r.Context(), now, MetricDown, map[string]float64{})
	}
	if wasDown {
		if err := m.o.Store.CloseOutages(r.Context(), now); err != nil {
			m.o.Log.Warn("connectivity: outage", "err", err)
		}
	}
	m.checkCap(r.Context(), now, c)
	reply(w, http.StatusOK, map[string]bool{"saved": true})
}

// Mux is what Register needs of api.Server.API.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// Register adds the routes.
func (m *Monitor) Register(mux Mux) {
	mux.HandleFunc("GET /api/v1/connectivity", m.handleGet)
	mux.HandleFunc("PUT /api/v1/connectivity/config", m.handleSave)
}
