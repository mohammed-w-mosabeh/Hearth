package broker

// Streaming Docker methods: Engine events and followed container logs.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
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

// Following logs: lines are cut at maxFollowLine and sent in batches of
// at most maxFollowBatch, at least every followFlush.
const (
	maxFollowLine  = 16 << 10
	maxFollowBatch = 500
	followFlush    = 200 * time.Millisecond
)

// containerLogsFollow streams a container's output from the last tail
// lines on: each item is a []LogLine. It ends when the container stops.
func (d *Docker) containerLogsFollow(ctx context.Context, _ Caller, p json.RawMessage, send func(any) error) error {
	v := struct {
		ID   string `json:"id"`
		Tail int    `json:"tail"`
	}{Tail: 200}
	if err := decodeParams(p, &v); err != nil {
		return err
	}
	if !ValidContainer(v.ID) {
		return badParam("id must be a container ID or name")
	}
	if v.Tail < 0 || v.Tail > 5000 {
		return badParam("tail must be 0–5000")
	}
	var info struct{ Config struct{ Tty bool } }
	if err := d.getInto(ctx, "/containers/"+esc(v.ID)+"/json", nil, &info); err != nil {
		return err
	}
	q := url.Values{"stdout": {"true"}, "stderr": {"true"}, "follow": {"true"}, "tail": {strconv.Itoa(v.Tail)}}
	resp, err := d.send(ctx, http.MethodGet, "/containers/"+esc(v.ID)+"/logs", q)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	lines := make(chan LogLine, 2*maxFollowBatch)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		readErr <- readLogStream(ctx, bufio.NewReaderSize(resp.Body, 32<<10), info.Config.Tty, lines)
	}()
	tick := time.NewTicker(followFlush)
	defer tick.Stop()
	batch := []LogLine{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := send(batch)
		batch = []LogLine{}
		return err
	}
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				if err := flush(); err != nil {
					return err
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := <-readErr; err != nil && !errors.Is(err, io.EOF) {
					return Errorf(CodeFailed, "docker: log stream ended")
				}
				return nil // the container stopped
			}
			if batch = append(batch, l); len(batch) >= maxFollowBatch {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-tick.C:
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

// readLogStream splits Docker's log stream (multiplexed frames, or raw
// output for a TTY) into lines until it ends.
func readLogStream(ctx context.Context, r *bufio.Reader, tty bool, out chan<- LogLine) error {
	partial := map[string][]byte{}
	emit := func(stream string, b []byte) bool {
		for len(b) > 0 {
			i := bytes.IndexByte(b, '\n')
			chunk := b
			if i >= 0 {
				chunk = b[:i]
			}
			cur := partial[stream]
			if room := maxFollowLine - len(cur); room > 0 {
				cur = append(cur, chunk[:min(len(chunk), room)]...)
			}
			partial[stream] = cur
			if i < 0 {
				return true
			}
			b = b[i+1:]
			text := strings.ToValidUTF8(strings.TrimRight(string(cur), "\r"), "�")
			partial[stream] = cur[:0]
			select {
			case out <- LogLine{Stream: stream, Text: text}:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	// Output cut off without a newline is still a line.
	defer func() {
		for _, stream := range []string{"stdout", "stderr"} {
			if len(partial[stream]) > 0 {
				emit(stream, []byte{'\n'})
			}
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		if tty {
			n, err := r.Read(buf)
			if !emit("stdout", buf[:n]) || err != nil {
				return err
			}
			continue
		}
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return err
		}
		stream := "stdout"
		if hdr[0] == 2 {
			stream = "stderr"
		}
		for n := int(binary.BigEndian.Uint32(hdr[4:8])); n > 0; {
			k, err := io.ReadFull(r, buf[:min(n, len(buf))])
			if !emit(stream, buf[:k]) || err != nil {
				return err
			}
			n -= k
		}
	}
}
