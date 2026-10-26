package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrUnavailable means the broker is not installed or not running.
var ErrUnavailable = errors.New("broker: not available")

// Client calls the broker from hearthd. Each call is its own connection,
// so an idle hearthd holds none (and the broker can exit).
type Client struct {
	Path string // default SocketPath
	ids  atomic.Int64
}

// Call runs method with params and decodes its result into out (if not
// nil). Errors: ErrUnavailable; ErrNotMember when the user is not in the
// hearth group (the socket refuses the connection, or the broker does);
// otherwise the broker's *Error.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	path := c.Path
	if path == "" {
		path = SocketPath
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	switch {
	case errors.Is(err, syscall.EACCES) || errors.Is(err, fs.ErrPermission):
		return ErrNotMember
	case err != nil:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(4 * time.Minute)) // a polkit dialog can take a while
	}
	req := Request{ID: c.ids.Add(1), Method: method}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			return err
		}
	}
	// A refused caller gets its answer and the connection closed at once,
	// possibly before the request is written: read the answer anyway.
	werr := json.NewEncoder(conn).Encode(req)
	line, err := bufio.NewReader(io.LimitReader(conn, MaxResponse)).ReadBytes('\n')
	if err != nil {
		if werr != nil {
			err = werr
		}
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("broker: bad response: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if werr != nil || resp.ID != req.ID {
		return errors.New("broker: response to another request")
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// Status is GET /api/v1/broker/status.
type Status struct {
	Available bool     `json:"available"`
	Member    bool     `json:"member"` // the user is in the hearth group
	Version   string   `json:"version,omitempty"`
	Methods   []string `json:"methods,omitempty"`
	Reason    string   `json:"reason,omitempty"` // "not-installed", "not-member", "error"
}

// HandleStatus serves GET /api/v1/broker/status.
func (c *Client) HandleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var st Status
	var ping PingResult
	switch err := c.Call(ctx, "broker.ping", nil, &ping); {
	case err == nil:
		st = Status{Available: true, Member: true, Version: ping.Version, Methods: ping.Methods}
	case errors.Is(err, ErrNotMember):
		st = Status{Available: true, Reason: "not-member"}
	case errors.Is(err, ErrUnavailable):
		st = Status{Reason: "not-installed"}
	default:
		st = Status{Reason: "error"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(st)
}
