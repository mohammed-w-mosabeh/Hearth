package power

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Auditor records actions (store.DB in production).
type Auditor interface {
	Audit(ctx context.Context, e store.AuditEntry) error
}

// HandleGet serves GET /api/v1/power.
func (w *Watcher) HandleGet(rw http.ResponseWriter, _ *http.Request) {
	writeJSON(rw, http.StatusOK, w.Snapshot())
}

// HandleSetProfile serves POST /api/v1/power/profile {"profile": "balanced"}.
// Switching profile is a reversible action: no confirmation, but audited.
func (w *Watcher) HandleSetProfile(audit Auditor) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Profile string `json:"profile"`
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Profile == "" {
			http.Error(rw, `body must be {"profile": "<name>"}`, http.StatusBadRequest)
			return
		}

		err = w.SetProfile(r.Context(), req.Profile)
		result := "ok"
		if err != nil {
			result = "error: " + err.Error()
			// A D-Bus error's text is only its message; keep the name too.
			if name := dbusx.ErrorName(err); name != "" {
				result = "error: " + name + ": " + err.Error()
			}
		}
		if aerr := audit.Audit(r.Context(), store.AuditEntry{
			Origin: store.OriginLocal, UID: os.Getuid(),
			Action: "power.set_profile", Target: "power:ppd:profile",
			Params: map[string]string{"profile": req.Profile}, Result: result,
		}); aerr != nil {
			http.Error(rw, "audit log unavailable", http.StatusInternalServerError)
			return
		}

		switch {
		case err == nil:
			writeJSON(rw, http.StatusOK, w.Snapshot().Profile)
		case errors.Is(err, ErrUnknownProfile):
			http.Error(rw, err.Error(), http.StatusBadRequest)
		case errors.Is(err, ErrNoProfiles):
			http.Error(rw, err.Error(), http.StatusServiceUnavailable)
		case isDenied(err):
			http.Error(rw, "not authorized to change the power profile", http.StatusForbidden)
		default:
			http.Error(rw, "power-profiles-daemon: "+err.Error(), http.StatusBadGateway)
		}
	}
}

func isDenied(err error) bool {
	name := dbusx.ErrorName(err)
	return name == "org.freedesktop.DBus.Error.AccessDenied" || strings.HasSuffix(name, ".NotAuthorized") ||
		strings.Contains(name, "PolicyKit")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
