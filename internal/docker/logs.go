package docker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// LogsPrefix starts the WebSocket topic of a container's live logs,
// "docker.logs:<id>". The output is followed only while someone
// subscribes.
const LogsPrefix = "docker.logs:"

// maxFollows bounds the containers followed at once (each holds a broker
// connection).
const maxFollows = 4

// followTail is how many earlier lines a follow starts with.
const followTail = 500

// LogsMsg is a message on a logs topic: lines (the first batch of a
// follow has Reset, so the page starts over), then "end" when the
// container stops or the follow fails.
type LogsMsg struct {
	Kind  string           `json:"kind"` // "lines" or "end"
	Reset bool             `json:"reset,omitempty"`
	Lines []broker.LogLine `json:"lines,omitempty"`
	Error string           `json:"error,omitempty"`
}

// Publisher is the subset of api.Hub the package needs.
type Publisher interface {
	Publish(topic string, data any) error
	Subscribers(topic string) int
	Topics(prefix string) map[string]int
	Watch(topic string) <-chan struct{}
}

// Logs follows the logs of the containers whose topics have subscribers.
type Logs struct {
	b   Broker
	pub Publisher
	log *slog.Logger
}

// NewLogs returns a Logs; call Run.
func NewLogs(b Broker, pub Publisher, log *slog.Logger) *Logs {
	if log == nil {
		log = slog.Default()
	}
	return &Logs{b: b, pub: pub, log: log}
}

type follow struct {
	subs   int
	gen    int
	cancel context.CancelFunc
}

type ended struct {
	topic string
	gen   int
}

// Run follows logs until ctx ends.
func (l *Logs) Run(ctx context.Context) {
	watch := l.pub.Watch(LogsPrefix)
	follows := map[string]*follow{}
	done := make(chan ended, maxFollows)
	var wg sync.WaitGroup
	defer wg.Wait()
	gen := 0
	update := func() {
		topics := l.pub.Topics(LogsPrefix)
		for t, f := range follows {
			if topics[t] == 0 {
				f.cancel()
				delete(follows, t)
			}
		}
		for t, n := range topics {
			f := follows[t]
			if f != nil && n <= f.subs {
				f.subs = n // fewer subscribers: keep going
				continue
			}
			// New, or a new subscriber who needs the earlier lines.
			if f != nil {
				f.cancel()
			} else if len(follows) >= maxFollows {
				_ = l.pub.Publish(t, LogsMsg{Kind: "end", Error: "too many logs open at once"})
				continue
			}
			gen++
			fctx, cancel := context.WithCancel(ctx)
			follows[t] = &follow{subs: n, gen: gen, cancel: cancel}
			wg.Add(1)
			go func(t string, gen int) {
				defer wg.Done()
				l.follow(fctx, t)
				select {
				case done <- ended{t, gen}:
				case <-ctx.Done():
				}
			}(t, gen)
		}
	}
	for {
		select {
		case <-ctx.Done():
			for _, f := range follows {
				f.cancel()
			}
			return
		case <-watch:
			update()
		case e := <-done:
			// An ended follow stays until its subscribers leave or a new
			// one comes, so a stopped container is not followed again.
			if f := follows[e.topic]; f != nil && f.gen == e.gen {
				f.cancel()
			}
		}
	}
}

// follow streams one container's logs to its topic.
func (l *Logs) follow(ctx context.Context, topic string) {
	id := strings.TrimPrefix(topic, LogsPrefix)
	if !broker.ValidContainer(id) {
		_ = l.pub.Publish(topic, LogsMsg{Kind: "end", Error: "not a container"})
		return
	}
	first := true
	err := l.b.Stream(ctx, "docker.containers.logs.follow", map[string]any{"id": id, "tail": followTail}, func(raw json.RawMessage) error {
		var lines []broker.LogLine
		if err := json.Unmarshal(raw, &lines); err != nil {
			return err
		}
		_ = l.pub.Publish(topic, LogsMsg{Kind: "lines", Reset: first, Lines: lines})
		first = false
		return nil
	})
	if ctx.Err() != nil {
		return // unsubscribed or restarted
	}
	end := LogsMsg{Kind: "end"}
	if first {
		end.Reset = true // no output at all
	}
	if err != nil {
		end.Error = errorText(err)
		l.log.Warn("docker: logs", "id", id, "err", err)
	}
	_ = l.pub.Publish(topic, end)
}

// errorText is a short message for the UI.
func errorText(err error) string {
	var be *broker.Error
	switch {
	case errors.Is(err, broker.ErrUnavailable):
		return "hearth-broker is not available"
	case errors.As(err, &be):
		return strings.TrimPrefix(be.Message, "docker: ")
	}
	return "unexpected answer from the broker"
}
