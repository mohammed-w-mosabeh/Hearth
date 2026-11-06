// Package proxy is the optional reverse proxy for managed apps (ROADMAP
// 9.13): http://<app>.localhost:<port>/ reaches a port the app publishes
// on this machine. It listens on 127.0.0.1 only (CLAUDE.md invariant 1;
// the LAN opt-in the roadmap named was dropped), is off until the owner
// turns it on, proxies only to 127.0.0.1 and a port the app's own spec
// publishes, and never to Hearth: hearthd's port is never a target, and
// hearthd refuses any Host but its own (SECURITY S3), so a name here
// cannot reach it either.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Setting is where the configuration is kept.
const Setting = "proxy"

// Port bounds: never a privileged port, which a user cannot bind.
const (
	MinPort     = 1024
	MaxPort     = 65535
	DefaultPort = 8470
	// MaxRoutes bounds the apps with a name.
	MaxRoutes = 64
	// maxConns bounds requests in flight.
	maxConns = 256
	// refresh is how often the apps' published ports are read again.
	refresh = 30 * time.Second
)

// Config is the owner's configuration.
type Config struct {
	Enabled bool           `json:"enabled"`
	Port    int            `json:"port"`
	Routes  map[string]int `json:"routes"` // app name to a port it publishes
}

// Check validates c against Hearth's own port.
func (c Config) Check(hearthPort int) error {
	if c.Port < MinPort || c.Port > MaxPort {
		return fmt.Errorf("the port must be %d to %d", MinPort, MaxPort)
	}
	if c.Port == hearthPort {
		return errors.New("the port is Hearth's own")
	}
	if len(c.Routes) > MaxRoutes {
		return fmt.Errorf("at most %d apps have a name", MaxRoutes)
	}
	for name, p := range c.Routes {
		if !spec.ValidName(name) {
			return errors.New("a route names no app")
		}
		if p < 1 || p > MaxPort || p == hearthPort || p == c.Port {
			return fmt.Errorf("%s: not a port it can reach", name)
		}
	}
	return nil
}

// Settings is where Config is kept (store.DB in production).
type Settings interface {
	Setting(ctx context.Context, key string) (json.RawMessage, bool, error)
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
}

// Targets lists each managed app's reachable ports: TCP ports its spec
// publishes on 127.0.0.1 (the default) or every address.
type Targets func(ctx context.Context) (map[string][]int, error)

// AppPorts reads them from a spec.
func AppPorts(s spec.Spec) []int {
	seen := map[int]bool{}
	for _, svc := range s.Services {
		for _, p := range svc.Ports {
			if p.Protocol != "" && p.Protocol != "tcp" {
				continue
			}
			if p.HostIP != "" && p.HostIP != "127.0.0.1" && p.HostIP != "0.0.0.0" {
				continue
			}
			if p.Host > 0 {
				seen[p.Host] = true
			}
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// Status is what GET /api/v1/proxy shows.
type Status struct {
	Config
	Running bool             `json:"running"`
	Error   string           `json:"error,omitempty"`
	Ports   map[string][]int `json:"ports"` // each app's reachable ports
}

// Proxy serves the names.
type Proxy struct {
	Settings   Settings
	Targets    Targets
	HearthPort func() int
	Log        *slog.Logger

	mu      sync.Mutex
	cfg     Config
	srv     *http.Server
	ln      net.Listener
	lnErr   string
	ports   map[string][]int
	readAt  time.Time
	sem     chan struct{}
	backend http.RoundTripper
}

func (p *Proxy) log() *slog.Logger {
	if p.Log == nil {
		return slog.Default()
	}
	return p.Log
}

// Start loads the saved configuration and listens if it is on.
func (p *Proxy) Start(ctx context.Context) {
	cfg := Config{Port: DefaultPort, Routes: map[string]int{}}
	if raw, ok, err := p.Settings.Setting(ctx, Setting); err == nil && ok {
		var saved Config
		if json.Unmarshal(raw, &saved) == nil && saved.Check(p.HearthPort()) == nil {
			cfg = saved
		}
	}
	if cfg.Routes == nil {
		cfg.Routes = map[string]int{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
	p.relisten()
}

// Stop closes the listener.
func (p *Proxy) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeLocked()
}

// Config returns the configuration.
func (p *Proxy) Config() Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return clone(p.cfg)
}

func clone(c Config) Config {
	r := make(map[string]int, len(c.Routes))
	for k, v := range c.Routes {
		r[k] = v
	}
	c.Routes = r
	return c
}

// Apply saves c and listens as it says.
func (p *Proxy) Apply(ctx context.Context, c Config) error {
	if c.Routes == nil {
		c.Routes = map[string]int{}
	}
	if err := c.Check(p.HearthPort()); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := p.Settings.SetSetting(ctx, Setting, raw); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.cfg
	p.cfg = clone(c)
	if old.Enabled != c.Enabled || old.Port != c.Port || (c.Enabled && p.ln == nil) {
		p.relisten()
	}
	if c.Enabled && p.ln == nil {
		return errors.New(p.lnErr)
	}
	return nil
}

// Status reports the configuration, the listener and the apps' ports.
func (p *Proxy) Status(ctx context.Context) Status {
	ports, _ := p.reachable(ctx, true)
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{Config: clone(p.cfg), Running: p.ln != nil, Error: p.lnErr, Ports: ports}
}

func (p *Proxy) closeLocked() {
	if p.srv != nil {
		_ = p.srv.Close()
	}
	p.srv, p.ln = nil, nil
}

// relisten closes the listener and opens it again if enabled. Called with
// p.mu held.
func (p *Proxy) relisten() {
	p.closeLocked()
	p.lnErr = ""
	if !p.cfg.Enabled {
		return
	}
	ln, err := new(net.ListenConfig).Listen(context.Background(), "tcp4", "127.0.0.1:"+strconv.Itoa(p.cfg.Port))
	if err != nil {
		p.lnErr = "cannot listen on 127.0.0.1:" + strconv.Itoa(p.cfg.Port) + ": the port is in use"
		p.log().Warn("proxy: listen", "port", p.cfg.Port, "err", err)
		return
	}
	if p.sem == nil {
		p.sem = make(chan struct{}, maxConns)
	}
	srv := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(p.log().Handler(), slog.LevelDebug),
	}
	p.srv, p.ln = srv, ln
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.log().Warn("proxy: serve", "err", err)
		}
	}()
}

// reachable returns the apps' ports, read at most every refresh unless
// fresh is set. For a request, a read that fails keeps the last answer for
// a minute; a fresh read (a plan, the status) must succeed.
func (p *Proxy) reachable(ctx context.Context, fresh bool) (map[string][]int, error) {
	p.mu.Lock()
	ports, at := p.ports, p.readAt
	p.mu.Unlock()
	if !fresh && ports != nil && time.Since(at) < refresh {
		return ports, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	got, err := p.Targets(rctx)
	if err != nil {
		if !fresh && ports != nil && time.Since(at) < 2*refresh {
			return ports, nil
		}
		return map[string][]int{}, err
	}
	p.mu.Lock()
	p.ports, p.readAt = got, time.Now()
	p.mu.Unlock()
	return got, nil
}

// Name is the app a Host header names for a listener on port: exactly
// "<app>.localhost:<port>", lower case; "" otherwise.
func Name(host string, port int) string {
	h, ok := strings.CutSuffix(strings.ToLower(host), ".localhost:"+strconv.Itoa(port))
	if !ok || !spec.ValidName(h) {
		return ""
	}
	return h
}

// target is where a request for name goes: the route's port, if the app
// still publishes it and it is neither Hearth's port nor the proxy's.
func (p *Proxy) target(ctx context.Context, name string) (int, string) {
	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()
	port, ok := cfg.Routes[name]
	if !ok {
		return 0, "No app has this name here."
	}
	if port == p.HearthPort() || port == cfg.Port {
		return 0, "No app has this name here."
	}
	ports, err := p.reachable(ctx, false)
	if err != nil {
		return 0, "Hearth cannot read its apps just now."
	}
	for _, q := range ports[name] {
		if q == port {
			return port, ""
		}
	}
	return 0, "This app no longer publishes the port its name points to."
}

func (p *Proxy) transport() http.RoundTripper {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.backend == nil {
		d := &net.Dialer{Timeout: 5 * time.Second}
		p.backend = &http.Transport{
			Proxy: nil, // never an environment proxy
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				// The URL is built below from a checked port; dial only
				// loopback whatever it says.
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return d.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", port))
			},
			MaxIdleConns:          32,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 0, // an app may take its time
			ForceAttemptHTTP2:     false,
		}
	}
	return p.backend
}

// ServeHTTP proxies one request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	default:
		plain(w, http.StatusServiceUnavailable, "Too many requests at once.")
		return
	}
	p.mu.Lock()
	own := p.cfg.Port
	p.mu.Unlock()
	name := Name(r.Host, own)
	if name == "" {
		plain(w, http.StatusNotFound, "Open http://<app>.localhost:"+strconv.Itoa(own)+"/ for an app Hearth gave a name.")
		return
	}
	port, why := p.target(r.Context(), name)
	if port == 0 {
		plain(w, http.StatusBadGateway, why)
		return
	}
	to := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(to)
			// The app sees the name it was asked by, so its own links and
			// trusted-host checks work.
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("Forwarded")
			pr.SetXForwarded()
		},
		Transport: p.transport(),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			p.log().Debug("proxy: backend", "app", name, "err", err)
			plain(w, http.StatusBadGateway, "The app does not answer on its port.")
		},
	}
	rp.ServeHTTP(w, r)
}

// plain writes a short message; nothing from the request is in it but a
// port number.
func plain(w http.ResponseWriter, status int, msg string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = bytes.NewBufferString(msg + "\n").WriteTo(w)
}
