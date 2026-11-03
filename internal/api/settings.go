package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"unicode/utf8"
)

// SettingsStore persists settings (store.DB in production).
type SettingsStore interface {
	Setting(ctx context.Context, key string) (json.RawMessage, bool, error)
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
}

// maxSettingBytes bounds one stored setting.
const maxSettingBytes = 16 << 10

// settingKeys is the closed list of settings the UI may read and write,
// each with a validator for its value.
var settingKeys = map[string]func(json.RawMessage) error{
	"home.layout":    validateHomeLayout,
	"home.snoozed":   validateSnoozed,
	"apps.favorites": validateFavorites,
}

// cardIDRe matches Home card IDs such as "cpu" or "disk:/home".
var cardIDRe = regexp.MustCompile(`^[a-z][a-z0-9]*(:[A-Za-z0-9._@:/ -]{1,190})?$`)

// validateHomeLayout accepts {"order": [ids], "hidden": [ids]}.
func validateHomeLayout(v json.RawMessage) error {
	var l struct {
		Order  []string `json:"order"`
		Hidden []string `json:"hidden"`
	}
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	// null would decode into an empty layout; require an object.
	if t := bytes.TrimSpace(v); len(t) == 0 || t[0] != '{' || dec.Decode(&l) != nil || !atEnd(dec) {
		return errors.New(`layout must be {"order": [...], "hidden": [...]}`)
	}
	if len(l.Order) > 256 || len(l.Hidden) > 256 {
		return errors.New("layout has too many cards")
	}
	for _, id := range append(l.Order, l.Hidden...) {
		if len(id) > 200 || !cardIDRe.MatchString(id) {
			return errors.New("layout has an invalid card id")
		}
	}
	return nil
}

// snoozeIDRe matches a Needs attention item ID (web/src/lib/health.ts):
// a source word, then optionally ":" and a printable subject such as a
// unit, a mount point or a listener.
var snoozeIDRe = regexp.MustCompile(`^[a-z][a-z.]{0,31}(:[^\x00-\x1f\x7f]{1,290})?$`)

// maxSnoozed bounds the snoozed items (MAX_SNOOZED in health.ts).
const maxSnoozed = 200

// validateSnoozed accepts {"until": {item id: Unix ms}}, Home's items
// hidden from Needs attention for 24 h (ROADMAP 8.6).
func validateSnoozed(v json.RawMessage) error {
	var s struct {
		Until map[string]int64 `json:"until"`
	}
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	if t := bytes.TrimSpace(v); len(t) == 0 || t[0] != '{' || dec.Decode(&s) != nil || !atEnd(dec) || s.Until == nil {
		return errors.New(`snoozed must be {"until": {"<item id>": <unix ms>}}`)
	}
	if len(s.Until) > maxSnoozed {
		return errors.New("too many snoozed items")
	}
	for id, until := range s.Until {
		if !snoozeIDRe.MatchString(id) || !utf8.ValidString(id) || until <= 0 {
			return errors.New("snoozed has an invalid item")
		}
	}
	return nil
}

// atEnd reports whether nothing but space follows the decoded value: the
// value is stored and served as it came, so it must be one JSON value.
func atEnd(dec *json.Decoder) bool {
	_, err := dec.Token()
	return err == io.EOF
}

// appIDRe matches desktop file IDs ("org.gnome.TextEditor", "Among Us",
// "appimage.obsidian-1.5.3"): printable, no path separators.
var appIDRe = regexp.MustCompile(`^[^/\x00-\x1f\x7f]{1,255}$`)

// validateFavorites accepts {"ids": [app ids]}.
func validateFavorites(v json.RawMessage) error {
	var f struct {
		IDs []string `json:"ids"`
	}
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	if t := bytes.TrimSpace(v); len(t) == 0 || t[0] != '{' || dec.Decode(&f) != nil || !atEnd(dec) {
		return errors.New(`favorites must be {"ids": [...]}`)
	}
	if len(f.IDs) > 500 {
		return errors.New("too many favorites")
	}
	for _, id := range f.IDs {
		if !appIDRe.MatchString(id) {
			return errors.New("favorites has an invalid app id")
		}
	}
	return nil
}

// SettingKeys lists the settings the UI owns, sorted.
func SettingKeys() []string {
	return slices.Sorted(maps.Keys(settingKeys))
}

// ValidateSetting checks a value for key as PUT would.
func ValidateSetting(key string, v json.RawMessage) error {
	validate, ok := settingKeys[key]
	if !ok {
		return errors.New("unknown setting")
	}
	if len(v) > maxSettingBytes {
		return errors.New("setting too large")
	}
	return validate(v)
}

// RegisterSettings adds GET and PUT /api/v1/settings/{key}. A key that was
// never saved reads as JSON null.
func (s *Server) RegisterSettings(st SettingsStore) {
	s.API.HandleFunc("GET /api/v1/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if _, ok := settingKeys[key]; !ok {
			http.Error(w, "unknown setting", http.StatusNotFound)
			return
		}
		v, ok, err := st.Setting(r.Context(), key)
		if err != nil {
			s.log.Warn("settings: read failed", "key", key, "err", err)
			http.Error(w, "settings unavailable", http.StatusInternalServerError)
			return
		}
		if !ok {
			v = json.RawMessage("null")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(v) // #nosec G705 -- JSON validated on write, served as application/json with nosniff and CSP
	})
	s.API.HandleFunc("PUT /api/v1/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		validate, ok := settingKeys[key]
		if !ok {
			http.Error(w, "unknown setting", http.StatusNotFound)
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, maxSettingBytes+1))
		if err != nil || len(b) > maxSettingBytes {
			http.Error(w, "setting too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err := validate(b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Store the compacted form so stored values never carry odd whitespace.
		var buf bytes.Buffer
		if err := json.Compact(&buf, b); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := st.SetSetting(r.Context(), key, buf.Bytes()); err != nil {
			s.log.Warn("settings: write failed", "key", key, "err", err)
			http.Error(w, "settings unavailable", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
