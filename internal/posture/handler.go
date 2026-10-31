package posture

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Auditor appends to the audit log (store.DB in production).
type Auditor interface {
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Mux is the subset of api.Mux the routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// Register adds GET /api/v1/security and POST /api/v1/security/cves.
func (s *Scanner) Register(mux Mux, audit Auditor) {
	mux.HandleFunc("GET /api/v1/security", s.handleScan)
	mux.HandleFunc("POST /api/v1/security/cves", s.handleCVEs(audit))
	mux.HandleFunc("GET /api/v1/restart", s.handleRestart)
}

func (s *Scanner) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.Scan(ctx))
}

// handleRestart is Home's reminder: it never waits for a scan.
func (s *Scanner) handleRestart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Restart(r.Context(), false))
}

// handleCVEs runs debsecan, which fetches the Debian security tracker's
// data (SECURITY S21c): only on this explicit request.
func (s *Scanner) handleCVEs(audit Auditor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		v, err := s.RunCVEs(ctx)
		if errors.Is(err, ErrBusy) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "debsecan is already running"})
			return
		}
		var result string
		switch {
		case err != nil:
			result = "error: " + err.Error()
		case !v.Installed:
			result = "error: debsecan is not installed"
		case v.Error != "":
			result = "error: " + v.Error
		default:
			result = "ok: " + strconv.Itoa(len(v.List)) + " CVEs"
		}
		if aerr := audit.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
			Action: "security.cves", Target: "debsecan", Result: result}); aerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "audit log unavailable"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "debsecan could not run"})
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
