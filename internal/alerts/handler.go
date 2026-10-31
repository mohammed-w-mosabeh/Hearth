package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// RuleStore is what the rule API needs (store.DB).
type RuleStore interface {
	AlertRules(ctx context.Context) ([]store.AlertRule, error)
	CreateAlertRule(ctx context.Context, r store.AlertRule) (int64, error)
	UpdateAlertRule(ctx context.Context, r store.AlertRule) error
	DeleteAlertRule(ctx context.Context, id int64) error
	AlertEvents(ctx context.Context, limit int, before int64) ([]store.AlertEvent, error)
	Audit(ctx context.Context, e store.AuditEntry) error
}

// API serves /api/v1/alerts/*.
type API struct {
	Store  RuleStore
	Engine *Engine
	Notify Notifier
	// Channels are the remote channels; nil has none.
	Channels *Channels
}

// Register adds the routes to mux.
func (a *API) Register(mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}) {
	mux.HandleFunc("GET /api/v1/alerts/rules", a.list)
	mux.HandleFunc("POST /api/v1/alerts/rules", a.create)
	mux.HandleFunc("PUT /api/v1/alerts/rules/{id}", a.update)
	mux.HandleFunc("DELETE /api/v1/alerts/rules/{id}", a.remove)
	mux.HandleFunc("GET /api/v1/alerts/events", a.events)
	mux.HandleFunc("GET /api/v1/alerts/metrics", a.metrics)
	mux.HandleFunc("POST /api/v1/alerts/test", a.test)
	mux.HandleFunc("GET /api/v1/alerts/channels", a.channels)
	mux.HandleFunc("PUT /api/v1/alerts/channels", a.saveChannels)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	rules, err := a.Store.AlertRules(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "rules could not be read")
		return
	}
	writeJSON(w, map[string]any{"rules": rules})
}

// decode reads a rule body; unknown fields are refused.
func decode(r *http.Request) (store.AlertRule, error) {
	var rule store.AlertRule
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return rule, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rule); err != nil {
		return rule, errors.New("body must be a rule object")
	}
	return rule, Validate(rule)
}

func (a *API) id(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, http.StatusBadRequest, "bad rule id")
		return 0, false
	}
	return id, true
}

// changed audits a rule change and reloads the engine.
func (a *API) changed(r *http.Request, action string, id int64, rule any) {
	_ = a.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
		Action: action, Target: "alert-rule:" + strconv.FormatInt(id, 10), Params: rule, Result: "ok"})
	if a.Engine != nil {
		_ = a.Engine.Reload(r.Context())
	}
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	rule, err := decode(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := a.Store.CreateAlertRule(r.Context(), rule)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "the rule could not be saved")
		return
	}
	rule.ID = id
	a.changed(r, "alerts.rule.create", id, rule)
	writeJSON(w, rule)
}

func (a *API) update(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	rule, err := decode(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule.ID = id
	switch err := a.Store.UpdateAlertRule(r.Context(), rule); {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusNotFound, "no such rule")
		return
	case err != nil:
		httpError(w, http.StatusInternalServerError, "the rule could not be saved")
		return
	}
	a.changed(r, "alerts.rule.update", id, rule)
	writeJSON(w, rule)
}

func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	switch err := a.Store.DeleteAlertRule(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusNotFound, "no such rule")
		return
	case err != nil:
		httpError(w, http.StatusInternalServerError, "the rule could not be deleted")
		return
	}
	a.changed(r, "alerts.rule.delete", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// events serves ?limit=N (1..500, default 100) &before=<id>, newest first.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	limit, before := 100, int64(0)
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 500 {
			httpError(w, http.StatusBadRequest, "limit must be 1 to 500")
			return
		}
		limit = n
	}
	if s := r.URL.Query().Get("before"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 {
			httpError(w, http.StatusBadRequest, "bad before")
			return
		}
		before = n
	}
	evs, err := a.Store.AlertEvents(r.Context(), limit, before)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "history could not be read")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"events": evs})
}

// metrics serves the catalog with the instances seen lately, for the form.
func (a *API) metrics(w http.ResponseWriter, _ *http.Request) {
	type entry struct {
		Metric
		Instances []string `json:"instances"`
	}
	out := make([]entry, 0, len(Metrics))
	for _, m := range Metrics {
		e := entry{Metric: m, Instances: []string{}}
		if a.Engine != nil && m.Instance != "" {
			e.Instances = a.Engine.Instances(m.ID)
		}
		out = append(out, e)
	}
	writeJSON(w, map[string]any{"metrics": out})
}

// test sends a sample notification so the user can see the channel works.
func (a *API) test(w http.ResponseWriter, r *http.Request) {
	if ch := r.URL.Query().Get("channel"); ch != "" {
		a.testChannel(w, r, ch)
		return
	}
	if a.Notify == nil {
		httpError(w, http.StatusServiceUnavailable, "no notification channel")
		return
	}
	if err := a.Notify.Notify(r.Context(), "Hearth test notification", "Alerts will appear like this.", "info"); err != nil {
		httpError(w, http.StatusBadGateway, "the desktop did not accept the notification")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testChannel sends a sample through a remote channel. The error is the
// channel's own, which holds no secret.
func (a *API) testChannel(w http.ResponseWriter, r *http.Request, channel string) {
	if a.Channels == nil || (channel != "ntfy" && channel != "smtp") {
		httpError(w, http.StatusBadRequest, "unknown channel")
		return
	}
	err := a.Channels.Send(r.Context(), channel, "Hearth test notification", "Alerts will arrive like this.", "info")
	result := "ok"
	if err != nil {
		result = "error"
	}
	_ = a.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(),
		Action: "alerts.channel.test", Target: "channel:" + channel, Result: result})
	if err != nil {
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) channels(w http.ResponseWriter, _ *http.Request) {
	if a.Channels == nil {
		httpError(w, http.StatusServiceUnavailable, "no channels")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, a.Channels.View())
}

// saveChannels audits which channels are on, and nothing of their settings.
func (a *API) saveChannels(w http.ResponseWriter, r *http.Request) {
	if a.Channels == nil {
		httpError(w, http.StatusServiceUnavailable, "no channels")
		return
	}
	var u ChannelUpdate
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&u); err != nil {
		httpError(w, http.StatusBadRequest, "body must be a channels object")
		return
	}
	if err := a.Channels.Update(u); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	v := a.Channels.View()
	_ = a.Store.Audit(r.Context(), store.AuditEntry{Origin: store.OriginLocal, UID: os.Getuid(), Action: "alerts.channels.update",
		Target: "channels", Params: map[string]bool{"ntfy": v.NTFY.Enabled, "smtp": v.SMTP.Enabled}, Result: "ok"})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, v)
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
