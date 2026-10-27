// Package homepage is the Homepage (SPEC 6, ROADMAP 2.9): tiles the user
// enters through a form, uploaded icons, and a health check of each
// tile's URL (SECURITY S21) while the page is open.
package homepage

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Topic carries tile status: {"kind":"status","status":{"<id>":Status}}.
const Topic = "homepage"

// Store is the subset of store.DB the package needs.
type Store interface {
	Tiles(ctx context.Context) ([]store.Tile, error)
	CreateTile(ctx context.Context, t store.Tile) (int64, error)
	UpdateTile(ctx context.Context, t store.Tile) error
	DeleteTile(ctx context.Context, id int64) error
	OrderTiles(ctx context.Context, ids []int64) error
	PutIcon(ctx context.Context, sha, mime string, data []byte) error
	Icon(ctx context.Context, sha string) (string, []byte, error)
	DropOrphanIcons(ctx context.Context, age time.Duration) error
}

// Publisher is the subset of api.Hub the checker needs.
type Publisher interface {
	Publish(topic string, data any) error
	Subscribers(topic string) int
	Watch(topic string) <-chan struct{}
}

// --- validation ---

var (
	builtinRe = regexp.MustCompile(`^builtin:[a-z0-9-]{1,32}$`)
	uploadRe  = regexp.MustCompile(`^upload:[a-f0-9]{64}$`)
)

// ValidURL checks a tile URL: http or https with a host, no credentials,
// at most 2048 bytes, no spaces or control characters.
func ValidURL(s string) error {
	if len(s) == 0 || len(s) > 2048 || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("the address must be 1–2048 characters without spaces")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("the address must start with http:// or https:// and name a host")
	}
	if u.User != nil {
		return errors.New("the address must not contain a user name or password")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return errors.New("the port must be 1–65535")
		}
	}
	return nil
}

func textOK(s string, minLen, maxLen int) bool {
	n := utf8.RuneCountInString(s)
	return utf8.ValidString(s) && n >= minLen && n <= maxLen &&
		strings.IndexFunc(s, unicode.IsControl) < 0
}

// validate checks a tile from the form; it trims text fields.
func validate(ctx context.Context, db Store, t *store.Tile) error {
	t.Name, t.Description, t.Group = strings.TrimSpace(t.Name), strings.TrimSpace(t.Description), strings.TrimSpace(t.Group)
	t.URL = strings.TrimSpace(t.URL)
	switch {
	case !textOK(t.Name, 1, 80):
		return errors.New("the name must be 1–80 characters")
	case !textOK(t.Description, 0, 200):
		return errors.New("the description must be at most 200 characters")
	case !textOK(t.Group, 0, 40):
		return errors.New("the group must be at most 40 characters")
	case t.Container != "" && !broker.ValidContainer(t.Container):
		return errors.New("not a container name")
	}
	if err := ValidURL(t.URL); err != nil {
		return err
	}
	switch {
	case t.Icon == "" || builtinRe.MatchString(t.Icon):
	case uploadRe.MatchString(t.Icon):
		if _, _, err := db.Icon(ctx, strings.TrimPrefix(t.Icon, "upload:")); err != nil {
			return errors.New("the uploaded icon is gone; upload it again")
		}
	default:
		return errors.New("not an icon")
	}
	return nil
}

// --- health check (SECURITY S21) ---

// Status is a tile's last health check.
type Status struct {
	State     string `json:"state"` // "up", "down" or "warn" (answers, but its certificate is not trusted)
	Code      int    `json:"code,omitempty"`
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	Time      int64  `json:"time"` // Unix ms
}

// Checker checks the URLs of tiles with health_check on, every Interval
// while Topic has subscribers.
type Checker struct {
	db       Store
	pub      Publisher
	log      *slog.Logger
	client   *http.Client
	Interval time.Duration

	mu      sync.Mutex
	status  map[int64]Status
	changed chan struct{}
}

// NewChecker returns a Checker; call Run.
func NewChecker(db Store, pub Publisher, log *slog.Logger) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{db: db, pub: pub, log: log, client: healthClient(nil), Interval: time.Minute,
		status: map[int64]Status{}, changed: make(chan struct{}, 1)}
}

// healthClient: 5 s, at most one redirect, to the same host; never
// credentials or cookies; a proxy from the environment is not used.
func healthClient(tr http.RoundTripper) *http.Client {
	if tr == nil {
		tr = &http.Transport{
			Proxy:                 nil,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			MaxIdleConns:          4,
			IdleConnTimeout:       90 * time.Second,
			DisableCompression:    true,
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 1 {
				return errors.New("more than one redirect")
			}
			if req.URL.Hostname() != via[0].URL.Hostname() {
				return errors.New("redirect to another host")
			}
			return nil
		},
	}
}

// maxHealthBody is how much of an answer is read before it is dropped.
const maxHealthBody = 64 << 10

// Check checks one URL now.
func (c *Checker) Check(ctx context.Context, raw string) Status {
	start := time.Now()
	st := Status{State: "down", Time: start.UnixMilli()}
	if ValidURL(raw) != nil {
		st.Error = "invalid address"
		return st
	}
	do := func(method string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, raw, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Hearth health check")
		return c.client.Do(req)
	}
	resp, err := do(http.MethodHead)
	if err == nil && (resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented) {
		_ = resp.Body.Close()
		resp, err = do(http.MethodGet)
	}
	st.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		var cert *tls.CertificateVerificationError
		if errors.As(err, &cert) {
			st.State, st.Error = "warn", "its certificate is not trusted"
			return st
		}
		st.Error = shortError(err)
		return st
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxHealthBody))
	_ = resp.Body.Close()
	st.Code = resp.StatusCode
	if resp.StatusCode >= 500 {
		st.Error = resp.Status
		return st
	}
	st.State = "up" // anything below 500 answers, including a login page (401, 403)
	return st
}

func shortError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "connection refused"):
		return "connection refused"
	case strings.Contains(s, "no such host"):
		return "unknown host"
	case strings.Contains(s, "Client.Timeout") || strings.Contains(s, "deadline exceeded"):
		return "no answer within 5 s"
	case strings.Contains(s, "redirect"):
		return "redirects elsewhere"
	}
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// Changed asks for a check soon (tiles were edited).
func (c *Checker) Changed() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// Snapshot returns the last status of each tile.
func (c *Checker) Snapshot() map[int64]Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int64]Status, len(c.status))
	for k, v := range c.status {
		out[k] = v
	}
	return out
}

// Run checks while Topic has subscribers, until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	watch := c.pub.Watch(Topic)
	var tick <-chan time.Time
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	subs := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch:
			n := c.pub.Subscribers(Topic)
			switch {
			case n > 0 && ticker == nil:
				ticker = time.NewTicker(c.Interval)
				tick = ticker.C
				c.round(ctx)
			case n == 0 && ticker != nil:
				ticker.Stop()
				ticker, tick = nil, nil
			case n > subs:
				c.publish() // a new subscriber
			}
			subs = n
		case <-c.changed:
			if ticker != nil {
				c.round(ctx)
			}
		case <-tick:
			c.round(ctx)
		}
	}
}

// round checks every enabled tile (four at a time) and publishes.
func (c *Checker) round(ctx context.Context) {
	tiles, err := c.db.Tiles(ctx)
	if err != nil {
		c.log.Warn("homepage: tiles", "err", err)
		return
	}
	type result struct {
		id int64
		st Status
	}
	results := make(chan result, len(tiles))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	keep := map[int64]bool{}
	for _, t := range tiles {
		if !t.HealthCheck {
			continue
		}
		keep[t.ID] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results <- result{t.ID, c.Check(ctx, t.URL)}
		}()
	}
	wg.Wait()
	close(results)
	c.mu.Lock()
	for id := range c.status {
		if !keep[id] {
			delete(c.status, id)
		}
	}
	for r := range results {
		c.status[r.id] = r.st
	}
	c.mu.Unlock()
	c.publish()
}

func (c *Checker) publish() {
	_ = c.pub.Publish(Topic, map[string]any{"kind": "status", "status": c.Snapshot()})
}

// --- HTTP ---

// API serves the Homepage routes.
type API struct {
	DB      Store
	Checker *Checker
}

// TileView is a tile as GET /api/v1/homepage returns it.
type TileView struct {
	store.Tile
	IconData string `json:"icon_data,omitempty"` // data: URI of an uploaded icon
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Register adds the routes to mux.
func (a *API) Register(mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}) {
	mux.HandleFunc("GET /api/v1/homepage", a.list)
	mux.HandleFunc("POST /api/v1/homepage/tiles", a.create)
	mux.HandleFunc("PUT /api/v1/homepage/tiles/{id}", a.update)
	mux.HandleFunc("DELETE /api/v1/homepage/tiles/{id}", a.remove)
	mux.HandleFunc("PUT /api/v1/homepage/order", a.order)
	mux.HandleFunc("POST /api/v1/homepage/icons", a.upload)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	tiles, err := a.DB.Tiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tiles could not be read")
		return
	}
	out := make([]TileView, 0, len(tiles))
	for _, t := range tiles {
		v := TileView{Tile: t}
		if sha, ok := strings.CutPrefix(t.Icon, "upload:"); ok {
			if mime, data, err := a.DB.Icon(r.Context(), sha); err == nil {
				v.IconData = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
			}
		}
		out = append(out, v)
	}
	status := map[int64]Status{}
	if a.Checker != nil {
		status = a.Checker.Snapshot()
	}
	writeJSON(w, http.StatusOK, map[string]any{"tiles": out, "status": status})
}

func (a *API) readTile(w http.ResponseWriter, r *http.Request) (store.Tile, bool) {
	var t store.Tile
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, "not a tile")
		return t, false
	}
	if err := validate(r.Context(), a.DB, &t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return t, false
	}
	return t, true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "bad tile id")
		return 0, false
	}
	return id, true
}

func (a *API) changed() {
	if a.Checker != nil {
		a.Checker.Changed()
	}
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	t, ok := a.readTile(w, r)
	if !ok {
		return
	}
	if tiles, err := a.DB.Tiles(r.Context()); err == nil && len(tiles) >= 500 {
		writeError(w, http.StatusBadRequest, "at most 500 tiles")
		return
	}
	id, err := a.DB.CreateTile(r.Context(), t)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the tile could not be saved")
		return
	}
	a.changed()
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (a *API) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, ok := a.readTile(w, r)
	if !ok {
		return
	}
	t.ID = id
	if err := a.DB.UpdateTile(r.Context(), t); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such tile")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "the tile could not be saved")
		return
	}
	a.changed()
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.DB.DeleteTile(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such tile")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "the tile could not be deleted")
		return
	}
	a.changed()
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) order(w http.ResponseWriter, r *http.Request) {
	var v struct {
		IDs []int64 `json:"ids"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil || len(v.IDs) > 500 {
		writeError(w, http.StatusBadRequest, "ids must list at most 500 tiles")
		return
	}
	if err := a.DB.OrderTiles(r.Context(), v.IDs); err != nil {
		writeError(w, http.StatusInternalServerError, "the order could not be saved")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// upload stores an icon: the body is the file itself.
func (a *API) upload(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxIcon+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "the upload failed")
		return
	}
	mime, data, err := CheckIcon(b)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sha := IconSHA(data)
	if err := a.DB.PutIcon(r.Context(), sha, mime, data); err != nil {
		writeError(w, http.StatusInternalServerError, "the icon could not be saved")
		return
	}
	// An icon whose form is never saved goes after a day.
	_ = a.DB.DropOrphanIcons(r.Context(), 24*time.Hour)
	writeJSON(w, http.StatusCreated, map[string]string{
		"icon": "upload:" + sha,
		"data": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
	})
}
