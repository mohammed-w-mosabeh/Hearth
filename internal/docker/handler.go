package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// HandleInspect serves GET /api/v1/docker/containers/{id}: Docker's
// inspect with secret env values masked by the broker, and the
// container's SECURITY S12 risks.
func HandleInspect(b Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !broker.ValidContainer(id) {
			writeError(w, http.StatusBadRequest, "not a container ID or name")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		var res json.RawMessage
		if err := b.Call(ctx, "docker.containers.inspect", map[string]string{"id": id}, &res); err != nil {
			var be *broker.Error
			status := http.StatusBadGateway
			switch {
			case errors.As(err, &be) && be.Code == broker.CodeNotFound:
				status = http.StatusNotFound
			case errors.Is(err, broker.ErrUnavailable), errors.Is(err, broker.ErrNoDocker):
				status = http.StatusServiceUnavailable
			case errors.Is(err, broker.ErrNotMember):
				status = http.StatusForbidden
			}
			writeError(w, status, errorText(err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(res)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
