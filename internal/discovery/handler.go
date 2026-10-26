package discovery

import (
	"encoding/json"
	"net/http"
	"sort"
)

// maxIconIDs bounds one icon batch.
const maxIconIDs = 60

// HandleList serves GET /api/v1/apps.
func (x *Index) HandleList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"apps": x.Apps()})
}

// HandleIcons serves GET /api/v1/apps/icons?id=a&id=b as {"a": "data:..."}.
// Icons are data: URIs because <img> cannot send the bearer key and the
// CSP allows img-src data:. An SVG shown through <img> cannot run script.
// Unknown or icon-less IDs are left out.
func (x *Index) HandleIcons(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query()["id"]
	if len(ids) == 0 || len(ids) > maxIconIDs {
		http.Error(w, "1 to 60 id= parameters required", http.StatusBadRequest)
		return
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if d := x.IconData(id); d != "" {
			out[id] = d
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, out)
}

// HandleRunning serves GET /api/v1/apps/running as {"running": [ids]}:
// the apps that have processes now. running lists app IDs from unit
// names (collect.RunningApps).
func (x *Index) HandleRunning(running func() []string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		m := x.Running(running())
		ids := make([]string, 0, len(m))
		for id := range m {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"running": ids})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
