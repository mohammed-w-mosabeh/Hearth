package modes

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Mux is the subset of api.Mux the routes need.
type Mux interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// StateStore reads what the database holds about modes (store.DB).
type StateStore interface {
	ModeArmings(ctx context.Context) ([]store.ModeArming, error)
	ActiveModes(ctx context.Context) ([]store.ActiveMode, error)
}

// View is a mode as the API shows it. Armed lists the indexes of its armed
// triggers; an arming for another version of the file does not count.
type View struct {
	Mode
	Armed []int `json:"armed"`
}

// Active is a mode switched on now.
type Active struct {
	ID    string    `json:"id"`
	Since time.Time `json:"since"`
	By    string    `json:"by"`
	// Changed is true when the file is no longer the one switched on.
	Changed bool `json:"changed"`
}

// List is GET /api/v1/modes.
type List struct {
	Modes  []View      `json:"modes"`
	Errors []FileError `json:"errors"`
	Active []Active    `json:"active"`
	// Approvals are stack steps automatic switches left for a click.
	Approvals []ApprovalView `json:"approvals"`
}

// HandleRegister adds the mode routes. approvals may be nil.
func (s *Set) HandleRegister(mux Mux, db StateStore, approvals *Approvals) {
	mux.HandleFunc("GET /api/v1/modes", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.list(r.Context(), db)
		if err == nil {
			out.Approvals = []ApprovalView{}
			if approvals != nil {
				out.Approvals, err = approvals.Pending(r.Context())
			}
		}
		if err != nil {
			http.Error(w, "cannot read modes", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

func (s *Set) list(ctx context.Context, db StateStore) (List, error) {
	armings, err := db.ModeArmings(ctx)
	if err != nil {
		return List{}, err
	}
	out := List{Modes: []View{}, Errors: s.Errors()}
	for _, m := range s.Modes() {
		v := View{Mode: m, Armed: []int{}}
		for _, a := range armings {
			if a.ModeID == m.ID && a.FileHash == m.Hash && a.Trigger < len(m.Triggers) {
				v.Armed = append(v.Armed, a.Trigger)
			}
		}
		out.Modes = append(out.Modes, v)
	}
	act, err := db.ActiveModes(ctx)
	if err != nil {
		return List{}, err
	}
	out.Active = []Active{}
	for _, a := range act {
		m, ok := s.Mode(a.ModeID)
		out.Active = append(out.Active, Active{ID: a.ModeID, Since: a.Since, By: a.By, Changed: !ok || m.Hash != a.FileHash})
	}
	return out, nil
}
