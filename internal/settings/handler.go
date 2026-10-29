package settings

import (
	"encoding/json"
	"net/http"
)

// HandleInfo serves GET /api/v1/system/info.
func (s *Settings) HandleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Info(r.Context()))
}

// HandleTimezones serves GET /api/v1/system/timezones.
func (s *Settings) HandleTimezones(w http.ResponseWriter, r *http.Request) {
	zones, err := s.Timezones(r.Context())
	if err != nil {
		http.Error(w, "timedated is not available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, map[string]any{"timezones": zones})
}

// HandleNetwork serves GET /api/v1/system/network.
func (s *Settings) HandleNetwork(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Network(r.Context()))
}

// HandleAudio serves GET /api/v1/system/audio.
func (s *Settings) HandleAudio(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Audio(r.Context()))
}

// HandleAutostart serves GET /api/v1/system/autostart.
func (s *Settings) HandleAutostart(w http.ResponseWriter, _ *http.Request) {
	if s.cfg.Autostart == nil {
		writeJSON(w, map[string]any{"entries": []any{}})
		return
	}
	writeJSON(w, map[string]any{"entries": s.cfg.Autostart.List()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	_ = json.NewEncoder(w).Encode(v)
}
