package profiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Mux is the subset of api.Mux the routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// Active is a profile's activation.
type Active struct {
	Since time.Time `json:"since"`
	By    string    `json:"by"`
	Undo  []Item    `json:"undo"`
}

// Profile is a profile as the API shows it.
type Profile struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Items    []Item  `json:"items"`
	AutoGame bool    `json:"auto_game"`
	Active   *Active `json:"active,omitempty"`
}

// HandleRegister adds the profile routes.
func (m *Manager) HandleRegister(mux Mux) {
	mux.HandleFunc("GET /api/v1/profiles", m.handleList)
	mux.HandleFunc("POST /api/v1/profiles", m.handleSave)
	mux.HandleFunc("PUT /api/v1/profiles/{id}", m.handleSave)
	mux.HandleFunc("DELETE /api/v1/profiles/{id}", m.handleDelete)
}

func view(p store.Profile) (Profile, error) {
	items, err := decodeItems(p.Items)
	if err != nil {
		return Profile{}, err
	}
	out := Profile{ID: p.ID, Name: p.Name, Items: items, AutoGame: p.AutoGame}
	if !p.ActivatedAt.IsZero() {
		undo, err := decodeItems(p.Undo)
		if err != nil {
			return Profile{}, err
		}
		out.Active = &Active{Since: p.ActivatedAt, By: p.ActivatedBy, Undo: undo}
	}
	return out, nil
}

func (m *Manager) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := m.cfg.Store.Profiles(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the profiles could not be read")
		return
	}
	out := make([]Profile, 0, len(list))
	for _, p := range list {
		v, err := view(p)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "the profiles could not be read")
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

// handleSave creates a profile, or changes one given {id}.
func (m *Manager) handleSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Items    []Item `json:"items"`
		AutoGame bool   `json:"auto_game"`
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, `body must be {"name": "...", "items": [...], "auto_game": bool}`)
		return
	}
	var id int64
	if s := r.PathValue("id"); s != "" {
		if id, err = strconv.ParseInt(s, 10, 64); err != nil || id <= 0 {
			httpError(w, http.StatusBadRequest, "not a profile ID")
			return
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := m.validate(req.Name, req.Items, req.AutoGame); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	list, err := m.cfg.Store.Profiles(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the profile could not be saved")
		return
	}
	for _, p := range list {
		if p.ID != id && strings.EqualFold(p.Name, req.Name) {
			httpError(w, http.StatusConflict, "there is a profile with this name")
			return
		}
	}
	text, err := encodeItems(req.Items)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the profile could not be saved")
		return
	}
	p := store.Profile{ID: id, Name: req.Name, Items: text, AutoGame: req.AutoGame}
	action := "profile.update"
	if id == 0 {
		action = "profile.create"
		id, err = m.cfg.Store.AddProfile(r.Context(), p)
	} else {
		err = m.cfg.Store.UpdateProfile(r.Context(), p)
	}
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such profile")
		return
	}
	if !m.audited(w, r, action, id, req, err, "the profile could not be saved") {
		return
	}
	saved, err := m.cfg.Store.Profile(r.Context(), id)
	if err == nil {
		var v Profile
		if v, err = view(saved); err == nil {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	httpError(w, http.StatusInternalServerError, "the profile could not be read")
}

func (m *Manager) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, http.StatusBadRequest, "not a profile ID")
		return
	}
	p, err := m.cfg.Store.Profile(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such profile")
		return
	}
	if err == nil && !p.ActivatedAt.IsZero() {
		httpError(w, http.StatusConflict, "deactivate this profile first: what it changed would not be restored")
		return
	}
	if err == nil {
		err = m.cfg.Store.DeleteProfile(r.Context(), id)
	}
	if m.audited(w, r, "profile.delete", id, nil, err, "the profile could not be deleted") {
		w.WriteHeader(http.StatusNoContent)
	}
}

// audited records an action; on any failure it answers and returns false.
func (m *Manager) audited(w http.ResponseWriter, r *http.Request, action string, id int64, params any, err error, failed string) bool {
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	if aerr := m.cfg.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
		Action: action, Target: strconv.FormatInt(id, 10), Params: params, Result: result}); aerr != nil {
		httpError(w, http.StatusInternalServerError, "audit log unavailable")
		return false
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, failed)
		return false
	}
	return true
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
