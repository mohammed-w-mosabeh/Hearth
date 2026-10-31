package space

import (
	"encoding/json"
	"net/http"
)

// HandleRegister adds the routes.
func (a *Advisor) HandleRegister(mux Mux) {
	mux.HandleFunc("GET /api/v1/space", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.Report(r.Context()))
	})
	mux.HandleFunc("GET /api/v1/space/large", func(w http.ResponseWriter, r *http.Request) {
		res, err := a.Large(r.Context())
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the home folder could not be searched"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
