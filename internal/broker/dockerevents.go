package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// DockerEvent is one item of the docker.events stream: what changed,
// without Docker's attributes (they carry every label of the object).
type DockerEvent struct {
	Type   string `json:"type"`   // container, image, network or volume
	Action string `json:"action"` // start, die, destroy, health_status: healthy, ...
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`  // container, volume or network name
	Image  string `json:"image,omitempty"` // a container's image
	Time   int64  `json:"time"`            // Unix ns
}

// maxEventLine bounds one event from Docker; longer ones are skipped.
const maxEventLine = 64 << 10

var eventTypes = []string{"container", "image", "network", "volume"}

// dockerEvents streams Engine events from now on. exec_* events are left
// out: a health check makes two of them every interval and they change
// nothing a list shows (health_status does).
func (d *Docker) dockerEvents(ctx context.Context, _ Caller, p json.RawMessage, send func(any) error) error {
	var none struct{}
	if err := decodeParams(p, &none); err != nil {
		return err
	}
	resp, err := d.send(ctx, http.MethodGet, "/events", url.Values{"filters": {filtersQ(map[string][]string{"type": eventTypes})}})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReaderSize(resp.Body, 16<<10)
	for {
		line, err := readEventLine(r)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return Errorf(CodeFailed, "docker: event stream ended")
		}
		if line == nil {
			continue // oversized
		}
		ev, ok := parseEvent(line)
		if !ok {
			continue
		}
		if err := send(ev); err != nil {
			return err
		}
	}
}

// readEventLine reads one line, returning nil for one over maxEventLine.
func readEventLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	over := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !over {
			if len(line)+len(chunk) > maxEventLine {
				over, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if over {
			return nil, nil
		}
		return line, nil
	}
}

func parseEvent(line []byte) (DockerEvent, bool) {
	var e struct {
		Type   string
		Action string
		Actor  struct {
			ID         string
			Attributes map[string]string
		}
		TimeNano int64 `json:"timeNano"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type == "" || e.Action == "" || strings.HasPrefix(e.Action, "exec_") {
		return DockerEvent{}, false
	}
	if !slices.Contains(eventTypes, e.Type) {
		return DockerEvent{}, false
	}
	ev := DockerEvent{
		Type:   e.Type,
		Action: truncate(e.Action, 100),
		ID:     truncate(e.Actor.ID, 300),
		Name:   truncate(e.Actor.Attributes["name"], 300),
		Time:   e.TimeNano,
	}
	if e.Type == "container" {
		ev.Image = truncate(e.Actor.Attributes["image"], 300)
	}
	return ev, true
}
