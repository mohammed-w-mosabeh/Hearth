package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/alerts"
	"github.com/mohammed-w-mosabeh/hearth/internal/api"
	"github.com/mohammed-w-mosabeh/hearth/internal/homepage"
	"github.com/mohammed-w-mosabeh/hearth/internal/profiles"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

const (
	format     = "hearth-settings"
	version    = 1
	maxFile    = 512 << 10 // under the API's body limit
	maxEntries = 512       // per section
)

// secretName is SECURITY S19's pattern, for what names a query parameter.
var secretName = regexp.MustCompile(`(?i)(pass|pwd|token|secret|key|auth|cred)`)

// File is the exported document. It holds only what the user chose: never
// channels (they keep their own file), the session token, history or state.
type File struct {
	Format       string                     `json:"format"`
	Version      int                        `json:"version"`
	Settings     map[string]json.RawMessage `json:"settings"`
	WeeklyReport bool                       `json:"weekly_report"`
	AlertRules   []Rule                     `json:"alert_rules"`
	Tiles        []Tile                     `json:"tiles"`
	Profiles     []Profile                  `json:"profiles"`
}

// Rule is an alert rule; rules match by name.
type Rule struct {
	Name      string  `json:"name"`
	Metric    string  `json:"metric"`
	Target    string  `json:"target"`
	Op        string  `json:"op"`
	Threshold float64 `json:"threshold"`
	ForSec    int     `json:"for_s"`
	Severity  string  `json:"severity"`
	Enabled   bool    `json:"enabled"`
	QuietFrom string  `json:"quiet_from"`
	QuietTo   string  `json:"quiet_to"`
}

// Tile is a Homepage tile; tiles match by name. An uploaded icon stays
// behind: Icon is then "", and importing keeps the tile's own.
type Tile struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Group       string `json:"group"`
	Container   string `json:"container"`
	HealthCheck bool   `json:"health_check"`
}

// Profile is a profile; profiles match by name. Its game trigger is left
// out on purpose: a file must not arm anything that runs by itself.
type Profile struct {
	Name  string          `json:"name"`
	Items []profiles.Item `json:"items"`
}

// cleanQuery drops the pairs named like a credential. It reads the raw query:
// url.Values silently skips a pair with a semicolon, which a server may honor.
func cleanQuery(raw string) (string, bool) {
	var kept []string
	dropped := false
	for _, pair := range strings.FieldsFunc(raw, func(r rune) bool { return r == '&' || r == ';' }) {
		name, _, _ := strings.Cut(pair, "=")
		if n, err := url.QueryUnescape(name); err == nil {
			name = n
		}
		if secretName.MatchString(name) {
			dropped = true
			continue
		}
		kept = append(kept, pair)
	}
	return strings.Join(kept, "&"), dropped
}

// cleanURL takes credentials out of a tile's address (S19): query parameters
// named like one, and a fragment that holds one.
func cleanURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	query, dropped := cleanQuery(u.RawQuery)
	if dropped {
		u.RawQuery = query
	}
	if secretName.MatchString(u.Fragment) {
		u.Fragment, u.RawFragment = "", ""
		dropped = true
	}
	if !dropped {
		return s
	}
	return u.String()
}

func exportIcon(icon string) string {
	if strings.HasPrefix(icon, "upload:") {
		return ""
	}
	return icon
}

func fileRule(r store.AlertRule) Rule {
	return Rule{r.Name, r.Metric, r.Target, r.Op, r.Threshold, r.ForSec, r.Severity, r.Enabled, r.QuietFrom, r.QuietTo}
}

func fileTile(t store.Tile) Tile {
	return Tile{t.Name, cleanURL(t.URL), t.Description, exportIcon(t.Icon), t.Group, t.Container, t.HealthCheck}
}

// Export builds the settings file.
func (m *Manager) Export(ctx context.Context) (File, error) {
	f := File{Format: format, Version: version, Settings: map[string]json.RawMessage{},
		AlertRules: []Rule{}, Tiles: []Tile{}, Profiles: []Profile{}}
	for _, k := range api.SettingKeys() {
		v, ok, err := m.cfg.DB.Setting(ctx, k)
		if err != nil {
			return f, err
		}
		if ok {
			f.Settings[k] = v
		}
	}
	var err error
	if m.cfg.Weekly != nil {
		if f.WeeklyReport, err = m.cfg.Weekly.Enabled(ctx); err != nil {
			return f, err
		}
	}
	rules, err := m.cfg.DB.AlertRules(ctx)
	if err != nil {
		return f, err
	}
	for _, r := range rules {
		f.AlertRules = append(f.AlertRules, fileRule(r))
	}
	tiles, err := m.cfg.DB.Tiles(ctx)
	if err != nil {
		return f, err
	}
	for _, t := range tiles {
		f.Tiles = append(f.Tiles, fileTile(t))
	}
	list, err := m.cfg.DB.Profiles(ctx)
	if err != nil {
		return f, err
	}
	for _, p := range list {
		var items []profiles.Item
		if err := json.Unmarshal([]byte(p.Items), &items); err != nil {
			return f, fmt.Errorf("backup: profile %d: %w", p.ID, err)
		}
		f.Profiles = append(f.Profiles, Profile{p.Name, items})
	}
	return f, nil
}

// HandleExport serves GET /api/v1/backup/export.
func (m *Manager) HandleExport(w http.ResponseWriter, r *http.Request) {
	f, err := m.Export(r.Context())
	if err != nil {
		m.cfg.Log.Warn("backup: export", "err", err)
		httpError(w, http.StatusInternalServerError, "the settings could not be exported")
		return
	}
	_ = m.cfg.DB.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
		Action: "hearth.export", Target: "settings", Result: "ok"})
	writeJSON(w, http.StatusOK, f)
}

// parse reads an untrusted file: this format and version, no unknown field.
func parse(s string) (File, error) {
	var f File
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, errors.New("this is not a Hearth settings file")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return f, errors.New("this is not a Hearth settings file")
	}
	switch {
	case f.Format != format:
		return f, errors.New("this is not a Hearth settings file")
	case f.Version != version:
		return f, fmt.Errorf("this file has version %d; this Hearth reads version %d", f.Version, version)
	case len(f.Settings) > maxEntries || len(f.AlertRules) > maxEntries || len(f.Tiles) > maxEntries || len(f.Profiles) > maxEntries:
		return f, errors.New("this file has too many entries")
	}
	return f, nil
}

// op is one previewed change, kept in the plan so that what runs is what was
// shown.
type op struct {
	item  actions.Item
	apply func(ctx context.Context) error
}

// collector gathers a plan. Item.Detail is "add" or "update"; the UI words it.
type collector struct {
	ops     []op
	blocked []actions.Blocked
}

func (c *collector) add(id, name, detail string, apply func(ctx context.Context) error) {
	c.ops = append(c.ops, op{actions.Item{ID: id, Name: name, Detail: detail}, apply})
}

func (c *collector) block(id, name, reason, why string) {
	c.blocked = append(c.blocked, actions.Blocked{Item: actions.Item{ID: id, Name: name, Detail: why}, Reason: reason})
}

func (m *Manager) planSettings(ctx context.Context, f File, c *collector) error {
	keys := make([]string, 0, len(f.Settings))
	for k := range f.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		id := "setting:" + k
		var buf bytes.Buffer
		if err := api.ValidateSetting(k, f.Settings[k]); err != nil {
			c.block(id, k, "invalid", err.Error())
			continue
		}
		if err := json.Compact(&buf, f.Settings[k]); err != nil {
			c.block(id, k, "invalid", "invalid JSON")
			continue
		}
		cur, ok, err := m.cfg.DB.Setting(ctx, k)
		if err != nil {
			return err
		}
		if ok && bytes.Equal(cur, buf.Bytes()) {
			continue
		}
		detail := "add"
		if ok {
			detail = "update"
		}
		value := json.RawMessage(buf.Bytes())
		c.add(id, k, detail, func(ctx context.Context) error { return m.cfg.DB.SetSetting(ctx, k, value) })
	}
	if m.cfg.Weekly != nil {
		on, err := m.cfg.Weekly.Enabled(ctx)
		if err != nil {
			return err
		}
		if on != f.WeeklyReport {
			want := f.WeeklyReport
			c.add("weekly:report", "weekly report", "update", func(ctx context.Context) error { return m.cfg.Weekly.SetEnabled(ctx, want) })
		}
	}
	return nil
}

func (m *Manager) planRules(ctx context.Context, f File, c *collector) error {
	cur, err := m.cfg.DB.AlertRules(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range f.AlertRules {
		id := "rule:" + r.Name
		s := store.AlertRule{Name: r.Name, Metric: r.Metric, Target: r.Target, Op: r.Op, Threshold: r.Threshold, ForSec: r.ForSec,
			Severity: r.Severity, Enabled: r.Enabled, QuietFrom: r.QuietFrom, QuietTo: r.QuietTo}
		if err := alerts.Validate(s); err != nil {
			c.block(id, r.Name, "invalid", err.Error())
			continue
		}
		if seen[r.Name] {
			c.block(id, r.Name, "invalid", "listed twice")
			continue
		}
		seen[r.Name] = true
		i := indexBy(cur, func(x store.AlertRule) string { return x.Name }, r.Name)
		if i < 0 {
			c.add(id, r.Name, "add", func(ctx context.Context) error {
				_, err := m.cfg.DB.CreateAlertRule(ctx, s)
				return err
			})
			continue
		}
		if fileRule(cur[i]) == r {
			continue
		}
		s.ID = cur[i].ID
		c.add(id, r.Name, "update", func(ctx context.Context) error { return m.cfg.DB.UpdateAlertRule(ctx, s) })
	}
	return nil
}

func (m *Manager) planTiles(ctx context.Context, f File, c *collector) error {
	cur, err := m.cfg.DB.Tiles(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, t := range f.Tiles {
		id := "tile:" + t.Name
		s := store.Tile{Name: t.Name, URL: t.URL, Description: t.Description, Icon: t.Icon, Group: t.Group, Container: t.Container, HealthCheck: t.HealthCheck}
		if err := homepage.Validate(ctx, m.cfg.DB, &s); err != nil {
			c.block(id, t.Name, "invalid", err.Error())
			continue
		}
		if seen[s.Name] {
			c.block(id, t.Name, "invalid", "listed twice")
			continue
		}
		seen[s.Name] = true
		// A tile opens this address and may be health-checked, so the preview
		// shows where it leads, not just what it is called.
		label := s.Name + " (" + cleanURL(s.URL) + ")"
		i := indexBy(cur, func(x store.Tile) string { return x.Name }, s.Name)
		if i < 0 {
			c.add(id, label, "add", func(ctx context.Context) error {
				_, err := m.cfg.DB.CreateTile(ctx, s)
				return err
			})
			continue
		}
		// What export takes out must not read as a change, or import would
		// drop the credential or the icon the tile still has.
		if s.URL == cleanURL(cur[i].URL) {
			s.URL = cur[i].URL
		}
		if s.Icon == "" {
			s.Icon = cur[i].Icon
		}
		same := cur[i]
		same.Position = 0
		s.ID, s.Position = cur[i].ID, 0
		if same == s {
			continue
		}
		c.add(id, label, "update", func(ctx context.Context) error { return m.cfg.DB.UpdateTile(ctx, s) })
	}
	return nil
}

func (m *Manager) planProfiles(ctx context.Context, f File, c *collector) error {
	cur, err := m.cfg.DB.Profiles(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range f.Profiles {
		id := "profile:" + p.Name
		if err := m.cfg.ValidateProfile(p.Name, p.Items); err != nil {
			c.block(id, p.Name, "invalid", err.Error())
			continue
		}
		if seen[p.Name] {
			c.block(id, p.Name, "invalid", "listed twice")
			continue
		}
		seen[p.Name] = true
		items, err := json.Marshal(p.Items)
		if err != nil {
			return err
		}
		i := indexBy(cur, func(x store.Profile) string { return x.Name }, p.Name)
		if i < 0 {
			c.add(id, p.Name, "add", func(ctx context.Context) error {
				_, err := m.cfg.DB.AddProfile(ctx, store.Profile{Name: p.Name, Items: string(items)})
				return err
			})
			continue
		}
		if cur[i].Items == string(items) {
			continue
		}
		if !cur[i].ActivatedAt.IsZero() { // its undo list belongs to the old items
			c.block(id, p.Name, "active", "")
			continue
		}
		s := cur[i]
		s.Items = string(items)
		c.add(id, p.Name, "update", func(ctx context.Context) error { return m.cfg.DB.UpdateProfile(ctx, s) })
	}
	return nil
}

func indexBy[T any](list []T, key func(T) string, want string) int {
	for i := range list {
		if key(list[i]) == want {
			return i
		}
	}
	return -1
}

func (m *Manager) plan(ctx context.Context, r actions.Request) (actions.Plan, error) {
	f, err := parse(r.Params["file"])
	if err != nil {
		return actions.Plan{}, actions.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	c := &collector{}
	for _, step := range []func(context.Context, File, *collector) error{m.planSettings, m.planRules, m.planTiles, m.planProfiles} {
		if err := step(ctx, f, c); err != nil {
			return actions.Plan{}, err
		}
	}
	items := make([]actions.Item, 0, len(c.ops))
	for _, o := range c.ops {
		items = append(items, o.item)
	}
	return actions.Plan{Summary: strconv.Itoa(len(items)), Items: items, Blocked: c.blocked, State: c.ops}, nil
}

func (m *Manager) run(ctx context.Context, _ actions.Request, plan actions.Plan) (actions.Result, error) {
	ops, _ := plan.State.([]op)
	var res actions.Result
	// The way back: nothing changes unless this copy exists.
	if _, err := m.Create(ctx); err != nil {
		return res, fmt.Errorf("no backup could be made first, so nothing was changed: %w", err)
	}
	for _, o := range ops {
		if err := o.apply(ctx); err != nil {
			m.cfg.Log.Warn("backup: import", "item", o.item.ID, "err", err)
			res.Failed = append(res.Failed, actions.Blocked{Item: o.item, Reason: "error"})
			continue
		}
		res.Done++
	}
	if m.cfg.RulesChanged != nil {
		m.cfg.RulesChanged(ctx)
	}
	if m.cfg.TilesChanged != nil {
		m.cfg.TilesChanged()
	}
	return res, nil
}

// Register adds hearth/import (previewed, S15) and hearth/backup.
func (m *Manager) Register(reg *actions.Registry) {
	reg.Register(actions.Action{
		Kind: "hearth", Name: "import", Risk: actions.Destructive,
		Validate: func(r actions.Request) error {
			if r.Target != "settings" || len(r.Params) != 1 || r.Params["file"] == "" {
				return errors.New(`target must be "settings" and params one "file"`)
			}
			if len(r.Params["file"]) > maxFile {
				return errors.New("this file is too large")
			}
			return nil
		},
		Plan: m.plan,
		Run:  m.run,
	})
	reg.Register(actions.Action{
		Kind: "hearth", Name: "backup", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if r.Target != "database" || len(r.Params) != 0 {
				return errors.New(`target must be "database" and params empty`)
			}
			return nil
		},
		Plan: func(context.Context, actions.Request) (actions.Plan, error) {
			return actions.Plan{Summary: "database", Items: []actions.Item{{ID: "database", Name: "database"}}}, nil
		},
		Run: func(ctx context.Context, _ actions.Request, _ actions.Plan) (actions.Result, error) {
			if _, err := m.Create(ctx); err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Done: 1}, nil
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
