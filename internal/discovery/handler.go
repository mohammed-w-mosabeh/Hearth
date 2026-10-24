package discovery

import (
	"encoding/json"
	"net/http"
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
