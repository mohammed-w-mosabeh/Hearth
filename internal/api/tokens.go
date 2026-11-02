package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
)

// ReadOnlyRoutes is everything a read-only token can reach (SECURITY S2a):
// exactly what the hearth CLI needs, method GET, compared to the whole path.
// It is a closed list rather than "any GET" because some GETs are not reads a
// script should have: /terminal and /docker/exec open shells, /backup/export
// hands over settings, /journal can quote secrets, and others probe the
// network. /metrics has a check of its own.
var ReadOnlyRoutes = map[string]bool{
	"/api/v1/session":       true,
	"/api/v1/metrics/live":  true,
	"/api/v1/net/sockets":   true,
	"/api/v1/alerts/events": true,
	"/api/v1/services":      true,
	"/api/v1/containers":    true,
}

// readOnlyAllowed reports whether a request bearing a read-only token may go
// on. Any Upgrade is refused, so a token never reaches a WebSocket.
func readOnlyAllowed(r *http.Request) bool {
	return r.Method == http.MethodGet && r.Header.Get("Upgrade") == "" && ReadOnlyRoutes[r.URL.Path]
}

// guard admits a session to everything and a read-only token only to
// ReadOnlyRoutes: 401 for no valid credential, 403 for a token out of bounds.
func (s *Server) guard(next http.Handler) http.Handler {
	session := s.cfg.Auth.Require(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Tokens.Valid(auth.KeyFromRequest(r)) {
			session.ServeHTTP(w, r)
			return
		}
		if !readOnlyAllowed(r) {
			http.Error(w, "a read-only token cannot do this", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MetricsFunc renders /metrics; ok is false while the user has it turned off.
type MetricsFunc func(ctx context.Context) (body []byte, ok bool)

// SetMetrics supplies /metrics; it answers 404 until this is called.
func (s *Server) SetMetrics(f MetricsFunc) { s.metrics.Store(&f) }

const openMetricsType = "application/openmetrics-text; version=1.0.0; charset=utf-8"

// serveMetrics is GET /metrics, for a read-only token and nothing else. The
// credential is checked first, so a stranger cannot tell whether it is on.
func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !s.cfg.Tokens.Valid(auth.KeyFromRequest(r)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="hearth"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f := s.metrics.Load()
	if f == nil {
		http.NotFound(w, r)
		return
	}
	body, ok := (*f)(r.Context())
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", openMetricsType)
	_, _ = w.Write(body) // #nosec G705 -- OpenMetrics text with escaped labels, not HTML; nosniff and CSP apply
}

// MetricsSetting is the settings key that holds whether /metrics is on. It
// is not one of settingKeys: those travel in exported settings files, and a
// file should not switch this on.
const MetricsSetting = "metrics.enabled"

// MetricsEnabled reads the setting; anything but a stored true is off.
func MetricsEnabled(ctx context.Context, st SettingsStore) bool {
	v, ok, err := st.Setting(ctx, MetricsSetting)
	return err == nil && ok && string(v) == "true"
}

// TokenAudit records a change to tokens; a change it cannot record is not made.
type TokenAudit func(ctx context.Context, action, target, result string) error

// RegisterTokens adds the Tokens page's routes. None is in ReadOnlyRoutes, so
// a token cannot list, make or revoke tokens, and none returns a token.
func (s *Server) RegisterTokens(st SettingsStore, audit TokenAudit) {
	s.API.HandleFunc("GET /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"tokens": s.cfg.Tokens.List(), "metrics": MetricsEnabled(r.Context(), st), "port": s.Port(),
		})
	})
	s.API.HandleFunc("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if !decodeStrict(w, r, &body) {
			return
		}
		if !auth.ValidTokenName(body.Name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a name is 1 to 32 lowercase letters, digits or hyphens"})
			return
		}
		if err := audit(r.Context(), "tokens.create", body.Name, "ok"); err != nil {
			s.tokenFailed(w, err)
			return
		}
		info, err := s.cfg.Tokens.Create(body.Name)
		switch {
		case errors.Is(err, auth.ErrTokenExists), errors.Is(err, auth.ErrTokenLimit):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case err != nil:
			s.tokenFailed(w, err)
		default:
			writeJSON(w, http.StatusCreated, info)
		}
	})
	s.API.HandleFunc("DELETE /api/v1/tokens/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !auth.ValidTokenName(name) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": auth.ErrNoToken.Error()})
			return
		}
		if err := audit(r.Context(), "tokens.revoke", name, "ok"); err != nil {
			s.tokenFailed(w, err)
			return
		}
		err := s.cfg.Tokens.Revoke(name)
		switch {
		case errors.Is(err, auth.ErrNoToken):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case err != nil:
			s.tokenFailed(w, err)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	s.API.HandleFunc("PUT /api/v1/tokens/metrics", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodeStrict(w, r, &body) {
			return
		}
		if body.Enabled == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled is required"})
			return
		}
		value, result := json.RawMessage("false"), "ok: off"
		if *body.Enabled {
			value, result = json.RawMessage("true"), "ok: on"
		}
		if err := audit(r.Context(), "tokens.metrics", "metrics", result); err != nil {
			s.tokenFailed(w, err)
			return
		}
		if err := st.SetSetting(r.Context(), MetricsSetting, value); err != nil {
			s.tokenFailed(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// tokenFailed logs the cause; the answer names nothing of the file system.
func (s *Server) tokenFailed(w http.ResponseWriter, err error) {
	s.log.Warn("tokens: change failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the change could not be made; see hearthd's log"})
}

// decodeStrict reads one JSON object with no unknown fields into v.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(r.Body)
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err = dec.Decode(v); err == nil && !atEnd(dec) {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return false
	}
	return true
}
