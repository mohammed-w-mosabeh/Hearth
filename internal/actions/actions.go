// Package actions runs typed, risk-rated actions behind one endpoint,
// POST /api/v1/actions/{kind}/{action} (ARCHITECTURE "Actions endpoint").
//
// Destructive actions are two-step (SECURITY S15): a preview returns what
// would happen and a short-lived, single-use token bound to the action,
// target and params; only a request carrying that token (and, for high
// impact, the confirmation text) executes. Every execution is written to
// the audit log (S18).
package actions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Risk is how carefully an action must be confirmed (SPEC "Action").
type Risk string

const (
	Safe        Risk = "safe"        // no confirmation
	Reversible  Risk = "reversible"  // no dialog; the UI offers Undo
	Destructive Risk = "destructive" // preview, then confirm with the token
)

// TokenTTL is how long a preview token stays valid.
const TokenTTL = 2 * time.Minute

// Item is one thing an action affects.
type Item struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// Blocked is an item the action will not touch, and why.
type Blocked struct {
	Item
	Reason string `json:"reason"`
}

// Plan is what an action would do.
type Plan struct {
	Summary string    `json:"summary"`
	Items   []Item    `json:"items"`
	Blocked []Blocked `json:"blocked"`
	// Confirm, if set, must be typed back to execute (high impact).
	Confirm string `json:"confirm,omitempty"`
	// State is carried from the preview to Run (e.g. the exact processes
	// that were shown), so the action does what the user confirmed.
	State any `json:"-"`
	// Risk, if set, raises the action's risk for this target (a critical
	// unit makes a reversible stop destructive). It never lowers it.
	Risk Risk `json:"-"`
}

// Result is what an action did.
type Result struct {
	Done   int       `json:"done"`
	Failed []Blocked `json:"failed"`
	// Blocked repeats the plan's blocked items: actions that run without
	// a preview report them here.
	Blocked []Blocked `json:"blocked"`
}

// Request is the parsed request an action sees.
type Request struct {
	Target string
	Params map[string]string
	// Client is the request's RemoteAddr, for actions that must not act
	// on the user's own browser.
	Client string
}

// Action is one registered action.
type Action struct {
	Kind, Name string
	Risk       Risk
	// Validate checks target and params before anything else.
	Validate func(r Request) error
	// Plan resolves what the action would affect. Required for
	// Destructive actions; others run Plan then Run in one request.
	Plan func(ctx context.Context, r Request) (Plan, error)
	// Run executes plan (from the preview for Destructive actions).
	Run func(ctx context.Context, r Request, plan Plan) (Result, error)
}

// Auditor records actions (store.DB in production).
type Auditor interface {
	Audit(ctx context.Context, e store.AuditEntry) error
}

type token struct {
	key     string // kind/action
	target  string
	params  string
	plan    Plan
	expires time.Time
}

// Registry holds actions and outstanding preview tokens.
type Registry struct {
	audit Auditor
	now   func() time.Time

	mu      sync.Mutex
	actions map[string]Action
	tokens  map[string]token
}

// NewRegistry returns an empty Registry.
func NewRegistry(audit Auditor) *Registry {
	return &Registry{audit: audit, now: time.Now, actions: map[string]Action{}, tokens: map[string]token{}}
}

var riskLevel = map[Risk]int{Safe: 0, Reversible: 1, Destructive: 2}

// effective is the risk of running a with plan p.
func effective(a Action, p Plan) Risk {
	if p.Risk != "" && riskLevel[p.Risk] > riskLevel[a.Risk] {
		return p.Risk
	}
	return a.Risk
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Register adds a; it panics on a malformed or duplicate action, which is
// a programming error.
func (r *Registry) Register(a Action) {
	if !nameRe.MatchString(a.Kind) || !nameRe.MatchString(a.Name) || a.Validate == nil || a.Plan == nil || a.Run == nil {
		panic(fmt.Sprintf("actions: bad action %s/%s", a.Kind, a.Name))
	}
	switch a.Risk {
	case Safe, Reversible, Destructive:
	default:
		panic("actions: bad risk " + string(a.Risk))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := a.Kind + "/" + a.Name
	if _, dup := r.actions[key]; dup {
		panic("actions: duplicate " + key)
	}
	r.actions[key] = a
}

// body is the request body.
type body struct {
	Target  string            `json:"target"`
	Params  map[string]string `json:"params,omitempty"`
	Preview bool              `json:"preview,omitempty"`
	Token   string            `json:"token,omitempty"`
	Confirm string            `json:"confirm,omitempty"`
}

// previewResponse is returned for preview: true.
type previewResponse struct {
	Plan
	Risk    Risk      `json:"risk"`
	Token   string    `json:"token,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
}

// Handle serves POST /api/v1/actions/{kind}/{action}.
func (r *Registry) Handle(w http.ResponseWriter, req *http.Request) {
	key := req.PathValue("kind") + "/" + req.PathValue("action")
	r.mu.Lock()
	a, ok := r.actions[key]
	r.mu.Unlock()
	if !ok {
		httpError(w, http.StatusNotFound, "unknown action")
		return
	}

	raw, err := io.ReadAll(req.Body)
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	var b body
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || b.Target == "" || len(b.Target) > 512 || len(b.Params) > 16 {
		httpError(w, http.StatusBadRequest, `body must be {"target": "...", "params": {...}, "preview": bool, "token": "...", "confirm": "..."}`)
		return
	}
	ar := Request{Target: b.Target, Params: b.Params, Client: req.RemoteAddr}
	if ar.Params == nil {
		ar.Params = map[string]string{}
	}
	if err := a.Validate(ar); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	params := canonical(ar.Params)

	if b.Preview {
		plan, err := a.Plan(req.Context(), ar)
		if err != nil {
			httpError(w, statusOf(err), err.Error())
			return
		}
		risk := effective(a, plan)
		resp := previewResponse{Plan: plan, Risk: risk}
		if risk == Destructive && len(plan.Items) > 0 {
			resp.Token, resp.Expires = r.issue(token{key: key, target: ar.Target, params: params, plan: plan})
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	var plan Plan
	if a.Risk == Destructive || b.Token != "" {
		t, err := r.redeem(b.Token, key, ar.Target, params)
		if err != nil {
			httpError(w, http.StatusConflict, err.Error())
			return
		}
		if t.plan.Confirm != "" && b.Confirm != t.plan.Confirm {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("type %q to confirm", t.plan.Confirm))
			return
		}
		plan = t.plan
	} else {
		if plan, err = a.Plan(req.Context(), ar); err != nil {
			httpError(w, statusOf(err), err.Error())
			return
		}
		if effective(a, plan) == Destructive {
			httpError(w, http.StatusConflict, "preview first: this action needs a valid preview token")
			return
		}
	}

	res, runErr := a.Run(req.Context(), ar, plan)
	outcome := "ok"
	switch {
	case runErr != nil:
		outcome = "error: " + runErr.Error()
	case len(res.Failed) > 0:
		outcome = fmt.Sprintf("partial: %d done, %d failed", res.Done, len(res.Failed))
	}
	if err := r.audit.Audit(req.Context(), store.AuditEntry{
		Origin: store.OriginLocal, UID: os.Getuid(), Action: a.Kind + "." + a.Name,
		Target: ar.Target, Params: ar.Params, Result: outcome,
	}); err != nil {
		// The action already ran; say so rather than hide it.
		httpError(w, http.StatusInternalServerError, "action ran but could not be written to the audit log")
		return
	}
	if runErr != nil {
		httpError(w, statusOf(runErr), runErr.Error())
		return
	}
	if res.Failed == nil {
		res.Failed = []Blocked{}
	}
	res.Blocked = plan.Blocked
	if res.Blocked == nil {
		res.Blocked = []Blocked{}
	}
	writeJSON(w, http.StatusOK, res)
}

func (r *Registry) issue(t token) (string, time.Time) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on Linux
	}
	id := base64.RawURLEncoding.EncodeToString(b[:])
	t.expires = r.now().Add(TokenTTL)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, old := range r.tokens { // drop expired tokens
		if r.now().After(old.expires) {
			delete(r.tokens, k)
		}
	}
	r.tokens[id] = t
	return id, t.expires
}

// redeem consumes a token; it must match the action, target and params.
func (r *Registry) redeem(id, key, target, params string) (token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tokens[id]
	if id == "" || !ok {
		return token{}, errors.New("preview first: this action needs a valid preview token")
	}
	delete(r.tokens, id) // single use, even on mismatch
	if r.now().After(t.expires) {
		return token{}, errors.New("preview expired: preview again")
	}
	if t.key != key || t.target != target || t.params != params {
		return token{}, errors.New("preview token does not match this action")
	}
	return t, nil
}

// canonical renders params in a stable order for token binding.
func canonical(p map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(p[k])
		b.WriteByte(0)
	}
	return b.String()
}

// Error carries an HTTP status for an action error.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// Errorf returns an *Error with status.
func Errorf(status int, format string, args ...any) error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, args...)}
}

func statusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return http.StatusInternalServerError
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
