package diskusage

import (
	"encoding/json"
	"errors"
	"net/http"
)

// HandleRoots serves GET /api/v1/disks/roots: the folders that can be
// explored.
func (s *Scanner) HandleRoots(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"roots": s.cfg.Roots()})
}

// HandleUsage serves GET /api/v1/disks/usage?path=<folder>[&fresh=1]: the
// folder's sizes, or the progress of measuring them ("running": poll again).
func (s *Scanner) HandleUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path := q.Get("path")
	if path == "" || len(path) > 4096 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	res, err := s.Get(path, q.Get("fresh") == "1")
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, res)
	case errors.Is(err, ErrOutside):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only your home and disks you own can be explored"})
	case IsNotExist(err):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such folder"})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the folder cannot be measured"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
