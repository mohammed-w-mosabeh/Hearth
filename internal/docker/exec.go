package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/coder/websocket"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// A container shell (SECURITY S12a) is one WebSocket per terminal,
// GET /api/v1/docker/exec/{id}?cols=&rows=, bridged to the broker's
// docker.containers.exec session. Binary frames carry terminal bytes both
// ways; text frames carry control: the browser sends {"resize":[cols,rows]},
// hearthd sends {"state":"auth"|"ready"|"exit","error":...}.

// ExecSession is an open broker session (*broker.Session).
type ExecSession interface {
	Recv() (json.RawMessage, error)
	Send(v any) error
	Close() error
}

// ExecConfig configures HandleExec.
type ExecConfig struct {
	Open  func(ctx context.Context, method string, params any) (ExecSession, error)
	Audit func(ctx context.Context, e store.AuditEntry) error
	UID   int
	Log   *slog.Logger
}

// maxExecFrame bounds one browser frame (a paste).
const maxExecFrame = 32 << 10

type execState struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// HandleExec serves the terminal WebSocket of a container shell.
func HandleExec(cfg ExecConfig) http.HandlerFunc {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		cols, rows := sizeParam(r, "cols", 80), sizeParam(r, "rows", 24)
		if !broker.ValidContainer(id) || cols == 0 || rows == 0 {
			writeError(w, http.StatusBadRequest, "bad container or terminal size")
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{auth.Subprotocol}})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if conn.Subprotocol() != auth.Subprotocol {
			_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol "+auth.Subprotocol+" required")
			return
		}
		conn.SetReadLimit(maxExecFrame)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		audit := func(result string) {
			if cfg.Audit == nil {
				return
			}
			if err := cfg.Audit(context.WithoutCancel(ctx), store.AuditEntry{
				Origin: store.OriginLocal, UID: cfg.UID, Action: "container/exec", Target: id, Result: result,
			}); err != nil {
				cfg.Log.Warn("docker: audit exec", "err", err)
			}
		}
		control := func(s execState) {
			b, _ := json.Marshal(s)
			_ = conn.Write(ctx, websocket.MessageText, b)
		}
		exit := func(err error) {
			s := execState{State: "exit"}
			if err != nil {
				s.Error = execError(err)
			}
			control(s)
			_ = conn.Close(websocket.StatusNormalClosure, "")
		}

		control(execState{State: "auth"})
		sess, err := cfg.Open(ctx, "docker.containers.exec", map[string]any{"id": id, "cols": cols, "rows": rows})
		if err != nil {
			audit("error: " + execError(err))
			exit(err)
			return
		}
		defer func() { _ = sess.Close() }()
		if raw, err := sess.Recv(); err != nil || !ready(raw) {
			if err == nil {
				err = errors.New("unexpected answer from the broker")
			}
			audit("refused: " + execError(err))
			exit(err)
			return
		}
		audit("ok: shell opened")
		defer audit("ended")
		control(execState{State: "ready"})

		// Browser → shell.
		go func() {
			defer cancel()
			for {
				typ, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				var in broker.ExecIn
				if typ == websocket.MessageBinary {
					in.In = data
				} else if json.Unmarshal(data, &in) != nil || in.Resize == nil || len(in.In) > 0 {
					continue
				}
				if sess.Send(in) != nil {
					return
				}
			}
		}()
		// Shell → browser.
		for {
			raw, err := sess.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) || ctx.Err() != nil {
					err = nil
				}
				exit(err)
				return
			}
			var out broker.ExecOut
			if json.Unmarshal(raw, &out) != nil || len(out.Out) == 0 {
				continue
			}
			if conn.Write(ctx, websocket.MessageBinary, out.Out) != nil {
				return
			}
		}
	}
}

func ready(raw json.RawMessage) bool {
	var out broker.ExecOut
	return json.Unmarshal(raw, &out) == nil && out.Ready
}

func sizeParam(r *http.Request, name string, def int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 1000 {
		return 0
	}
	return n
}

// execError is a short message for the terminal.
func execError(err error) string {
	switch {
	case errors.Is(err, broker.ErrUnauthorized):
		return "not authorized: the password dialog was cancelled or refused"
	case errors.Is(err, broker.ErrNotMember):
		return "you are not in the hearth group"
	}
	return errorText(err)
}
