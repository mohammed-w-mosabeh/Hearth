package reports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Auditor appends to the audit log (store.DB in production).
type Auditor interface {
	Audit(ctx context.Context, e store.AuditEntry) error
}

// API serves reports.
type API struct {
	B      *Builder
	Weekly *Weekly
	Audit  Auditor
}

// itemRe matches the process group IDs that usage is recorded for.
var itemRe = regexp.MustCompile(`^(app|unit|user-unit|container):[^\p{Cc}]{1,255}$`)

// Mux is the subset of api.Mux the reports routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// Register adds the reports routes.
func (a *API) Register(mux Mux) {
	mux.HandleFunc("GET /api/v1/reports/machine", a.handleMachine)
	mux.HandleFunc("GET /api/v1/reports/item", a.handleItem)
	mux.HandleFunc("GET /api/v1/reports/items", a.handleItems)
	mux.HandleFunc("GET /api/v1/reports/weekly", a.handleWeekly)
	mux.HandleFunc("POST /api/v1/reports/weekly", a.handleSetWeekly)
	mux.HandleFunc("GET /api/v1/reports/saved/{name}", a.handleSaved)
}

func (a *API) handleMachine(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	m, err := a.B.Machine(r.Context(), period)
	if err != nil {
		reportError(w, err)
		return
	}
	stamp := m.To.Local().Format("2006-01-02")
	switch r.URL.Query().Get("format") {
	case "", "json":
		writeJSON(w, m)
	case "html":
		b, err := MachineHTML(m)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "cannot render the report")
			return
		}
		download(w, "hearth-"+period+"-"+stamp+".html", "text/html; charset=utf-8", b)
	case "md":
		download(w, "hearth-"+period+"-"+stamp+".md", "text/markdown; charset=utf-8", MachineMarkdown(m))
	default:
		httpError(w, http.StatusBadRequest, "format must be json, html or md")
	}
}

func (a *API) handleItem(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, period := q.Get("id"), q.Get("period")
	if !itemRe.MatchString(id) {
		httpError(w, http.StatusBadRequest, "id must be an app, unit, user-unit or container ID")
		return
	}
	it, err := a.B.Item(r.Context(), id, period)
	if err != nil {
		reportError(w, err)
		return
	}
	stamp := it.To.Local().Format("2006-01-02")
	switch q.Get("format") {
	case "", "json":
		writeJSON(w, it)
	case "html":
		b, err := ItemHTML(it)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "cannot render the report")
			return
		}
		download(w, "hearth-item-"+period+"-"+stamp+".html", "text/html; charset=utf-8", b)
	case "md":
		download(w, "hearth-item-"+period+"-"+stamp+".md", "text/markdown; charset=utf-8", ItemMarkdown(it))
	default:
		httpError(w, http.StatusBadRequest, "format must be json, html or md")
	}
}

// handleItems lists the items with recorded usage, busiest first, for
// the item report's picker.
func (a *API) handleItems(w http.ResponseWriter, r *http.Request) {
	to := a.B.cfg.Clock()
	items, err := a.B.cfg.Store.TopItems(r.Context(), to.Add(-usageKept), to, 500)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "usage history unavailable")
		return
	}
	writeJSON(w, items)
}

// usageKept is how long hourly item usage is kept (collect's usageKeep).
const usageKept = 90 * 24 * time.Hour

func (a *API) handleWeekly(w http.ResponseWriter, r *http.Request) {
	st, err := a.Weekly.State(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "settings unavailable")
		return
	}
	writeJSON(w, map[string]any{"enabled": st.Enabled, "last": st.Last, "saved": a.Weekly.List()})
}

func (a *API) handleSetWeekly(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Enabled == nil {
		httpError(w, http.StatusBadRequest, `body must be {"enabled": true|false}`)
		return
	}
	st, err := a.Weekly.SetEnabled(r.Context(), *req.Enabled)
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	if aerr := a.Audit.Audit(r.Context(), store.AuditEntry{
		Origin: store.OriginLocal, UID: os.Getuid(),
		Action: "reports.weekly", Target: "reports:weekly",
		Params: map[string]bool{"enabled": *req.Enabled}, Result: result,
	}); aerr != nil {
		httpError(w, http.StatusInternalServerError, "audit log unavailable")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "settings unavailable")
		return
	}
	if st.Enabled {
		a.Weekly.Kick() // the first report is due now, not in an hour
	}
	writeJSON(w, map[string]any{"enabled": st.Enabled, "last": st.Last})
}

func (a *API) handleSaved(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	b, ok := a.Weekly.Read(name)
	if !ok {
		httpError(w, http.StatusNotFound, "no such report")
		return
	}
	download(w, name, "text/html; charset=utf-8", b)
}

// download sends a report as a file. The UI fetches it with the session
// key and saves it; the page's own CSP still applies if it is opened.
func download(w http.ResponseWriter, name, ctype string, b []byte) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func reportError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrPeriod) {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	httpError(w, http.StatusInternalServerError, "history unavailable")
}

func httpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
