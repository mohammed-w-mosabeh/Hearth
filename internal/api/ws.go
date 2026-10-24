package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
)

const (
	wsReadLimit  = 4 << 10
	wsQueueLen   = 64
	wsWriteLimit = 5 * time.Second
	wsMaxTopics  = 32
)

var topicRe = regexp.MustCompile(`^[a-z][a-z0-9.]*(:[A-Za-z0-9._@:-]{1,200})?$`)

// clientMsg is what the browser sends: {"op":"sub"|"unsub","topic":"metrics"}.
type clientMsg struct {
	Op    string `json:"op"`
	Topic string `json:"topic"`
}

// serverMsg is what the server sends for each published update.
type serverMsg struct {
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
}

// Hub fans published messages out to subscribed WebSocket clients.
type Hub struct {
	log *slog.Logger

	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

type wsClient struct {
	conn   *websocket.Conn
	send   chan []byte
	topics map[string]struct{} // guarded by Hub.mu
	cancel context.CancelFunc
}

func newHub(log *slog.Logger) *Hub {
	return &Hub{log: log, clients: make(map[*wsClient]struct{})}
}

// Publish sends data (marshalled as JSON) to every client subscribed to
// topic. A client whose queue is full is disconnected rather than blocking
// the publisher.
func (h *Hub) Publish(topic string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	msg, err := json.Marshal(serverMsg{Topic: topic, Data: raw})
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if _, ok := c.topics[topic]; !ok {
			continue
		}
		select {
		case c.send <- msg:
		default:
			h.log.Warn("ws client too slow, disconnecting")
			c.cancel()
		}
	}
	return nil
}

// Subscribers reports how many clients follow topic, so collectors can
// switch between fast and slow sampling.
func (h *Hub) Subscribers(topic string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for c := range h.clients {
		if _, ok := c.topics[topic]; ok {
			n++
		}
	}
	return n
}

func (h *Hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		c.cancel()
	}
}

// serveWS runs behind auth.Require and the Host/Origin middleware.
func (h *Hub) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{auth.Subprotocol},
		// Origin is also checked by our middleware; keep the library's
		// same-origin check as a second layer.
	})
	if err != nil {
		return // Accept already wrote the HTTP error
	}
	if conn.Subprotocol() != auth.Subprotocol {
		_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol "+auth.Subprotocol+" required")
		return
	}
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &wsClient{conn: conn, send: make(chan []byte, wsQueueLen), topics: map[string]struct{}{}, cancel: cancel}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		_ = conn.CloseNow()
	}()

	go h.readLoop(ctx, c)
	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusGoingAway, "")
			return
		case msg := <-c.send:
			wctx, wcancel := context.WithTimeout(ctx, wsWriteLimit)
			err := conn.Write(wctx, websocket.MessageText, msg)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

func (h *Hub) readLoop(ctx context.Context, c *wsClient) {
	defer c.cancel()
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		var m clientMsg
		if typ != websocket.MessageText || json.Unmarshal(data, &m) != nil || !topicRe.MatchString(m.Topic) {
			_ = c.conn.Close(websocket.StatusPolicyViolation, "bad message")
			return
		}
		h.mu.Lock()
		switch m.Op {
		case "sub":
			if len(c.topics) < wsMaxTopics {
				c.topics[m.Topic] = struct{}{}
			}
		case "unsub":
			delete(c.topics, m.Topic)
		default:
			h.mu.Unlock()
			_ = c.conn.Close(websocket.StatusPolicyViolation, "bad op")
			return
		}
		h.mu.Unlock()
	}
}
