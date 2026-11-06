package changes

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
	"unicode/utf8"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Mux is the subset of api.Mux the routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// Limits of the API.
const (
	defaultPage = 300
	maxPage     = 1000
	maxLabel    = 80
)

// Register adds the change timeline routes.
func (t *Tracker) Register(mux Mux) {
	mux.HandleFunc("GET /api/v1/changes", t.handleTimeline)
	mux.HandleFunc("GET /api/v1/markers", t.handleMarkers)
	mux.HandleFunc("GET /api/v1/changes/snapshots", t.handleSnapshots)
	mux.HandleFunc("POST /api/v1/changes/snapshots", t.handleTake)
	mux.HandleFunc("DELETE /api/v1/changes/snapshots/{id}", t.handleDelete)
	mux.HandleFunc("GET /api/v1/changes/diff", t.handleDiff)
}

var validSource = map[string]bool{SourcePackages: true, SourceUnits: true, SourceContainers: true, SourceListeners: true, SourceApps: true, SourceHearth: true, SourceSystem: true}

// handleTimeline serves GET /api/v1/changes?before=<ms>&source=a,b&limit=n.
func (t *Tracker) handleTimeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	to := t.cfg.Clock().Add(time.Minute)
	if b := q.Get("before"); b != "" {
		ms, err := strconv.ParseInt(b, 10, 64)
		if err != nil || ms <= 0 {
			httpError(w, http.StatusBadRequest, "before must be unix milliseconds")
			return
		}
		to = time.UnixMilli(ms)
	}
	limit := defaultPage
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > maxPage {
			httpError(w, http.StatusBadRequest, "limit must be 1 to 1000")
			return
		}
		limit = n
	}
	var sources []string
	if s := q.Get("source"); s != "" {
		for _, src := range strings.Split(s, ",") {
			if !validSource[src] {
				httpError(w, http.StatusBadRequest, "unknown source")
				return
			}
			sources = append(sources, src)
		}
	}
	events, next, err := t.Timeline(r.Context(), time.UnixMilli(0), to, sources, limit)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the timeline could not be read")
		return
	}
	out := map[string]any{"events": events}
	if next != nil {
		out["next"] = next.UnixMilli()
	}
	writeJSON(w, http.StatusOK, out)
}

func (t *Tracker) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	list, err := t.cfg.Store.Snapshots(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the snapshots could not be read")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleTake serves POST /api/v1/changes/snapshots {"label": "..."}.
func (t *Tracker) handleTake(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, `body must be {"label": "<name>"}`)
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if !validLabel(req.Label) {
		httpError(w, http.StatusBadRequest, "a label is 1 to 80 characters on one line")
		return
	}
	snap, _, err := t.Take(r.Context(), req.Label, false)
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	if aerr := t.cfg.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
		Action: "changes.snapshot", Target: "snapshot:" + strconv.FormatInt(snap.ID, 10), Params: req, Result: result}); aerr != nil {
		httpError(w, http.StatusInternalServerError, "audit log unavailable")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the snapshot could not be taken")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func validLabel(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > maxLabel || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// handleDelete deletes one snapshot (Hearth's own record, not the system).
func (t *Tracker) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, http.StatusBadRequest, "not a snapshot ID")
		return
	}
	err = t.cfg.Store.DeleteSnapshot(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such snapshot")
		return
	}
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	if aerr := t.cfg.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
		Action: "changes.delete_snapshot", Target: "snapshot:" + strconv.FormatInt(id, 10), Result: result}); aerr != nil {
		httpError(w, http.StatusInternalServerError, "audit log unavailable")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the snapshot could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDiff serves GET /api/v1/changes/diff?from=<id>&to=<id|now>.
func (t *Tracker) handleDiff(w http.ResponseWriter, r *http.Request) {
	id := func(s string) (int64, bool) {
		if s == "now" {
			return 0, true
		}
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil && n > 0
	}
	from, ok1 := id(r.URL.Query().Get("from"))
	to, ok2 := id(r.URL.Query().Get("to"))
	if !ok1 || !ok2 {
		httpError(w, http.StatusBadRequest, "from and to must be snapshot IDs or now")
		return
	}
	a, at, err := t.SnapshotState(r.Context(), from)
	if err == nil {
		var b State
		var bt time.Time
		b, bt, err = t.SnapshotState(r.Context(), to)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"from": at, "to": bt, "sections": Diff(a, b)})
			return
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such snapshot")
		return
	}
	httpError(w, http.StatusInternalServerError, "the snapshots could not be read")
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
