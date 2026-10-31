package broker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Container exec shell (SECURITY S12a): a fixed /bin/sh with a TTY in a
// running container without host access, authorized by polkit for every
// session. The caller chooses only the container and the terminal size.

// ExecShell is the only command an exec session runs.
const ExecShell = "/bin/sh"

// maxExecs bounds open exec sessions.
const maxExecs = 2

// ExecOut is an item of an exec session: Ready once the shell runs,
// then its output.
type ExecOut struct {
	Ready bool   `json:"ready,omitempty"`
	Out   []byte `json:"out,omitempty"`
}

// ExecIn is a line the caller sends: input or a new terminal size.
type ExecIn struct {
	In     []byte  `json:"in,omitempty"`
	Resize *[2]int `json:"resize,omitempty"` // cols, rows
}

func validSize(cols, rows int) bool { return cols >= 1 && cols <= 1000 && rows >= 1 && rows <= 1000 }

func (d *Docker) containerExec(ctx context.Context, _ Caller, p json.RawMessage, in <-chan json.RawMessage, send func(any) error) error {
	v := struct {
		ID   string `json:"id"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}{Cols: 80, Rows: 24}
	if err := decodeParams(p, &v); err != nil {
		return err
	}
	if !ValidContainer(v.ID) {
		return badParam("id must be a container ID or name")
	}
	if !validSize(v.Cols, v.Rows) {
		return badParam("cols and rows must be 1–1000")
	}
	if d.execs.Add(1) > maxExecs {
		d.execs.Add(-1)
		return Errorf(CodeRefused, "too many container shells open")
	}
	defer d.execs.Add(-1)

	var info struct {
		State      struct{ Running, Paused bool }
		HostConfig HostConfig
	}
	if err := d.getInto(ctx, "/containers/"+esc(v.ID)+"/json", nil, &info); err != nil {
		return err
	}
	if !info.State.Running || info.State.Paused {
		return Errorf(CodeConflict, "the container is not running")
	}
	if risks := Risks(info.HostConfig); len(risks) > 0 {
		return Errorf(CodeRefused, "a shell in this container would reach the host (%s)", strings.Join(risks, ", "))
	}

	size := [2]int{v.Rows, v.Cols} // Docker's ConsoleSize is height, width
	b, err := d.doBody(ctx, http.MethodPost, "/containers/"+esc(v.ID)+"/exec", map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true,
		"Cmd": []string{ExecShell}, "Env": []string{"TERM=xterm-256color"}, "ConsoleSize": size,
	})
	if err != nil {
		return err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if json.Unmarshal(b, &created) != nil || !hexID.MatchString(created.ID) {
		return Errorf(CodeFailed, "docker: unexpected answer")
	}
	execPath := "/exec/" + esc(created.ID)
	resp, err := d.sendBody(ctx, http.MethodPost, execPath+"/start", nil,
		map[string]any{"Detach": false, "Tty": true, "ConsoleSize": size},
		http.Header{"Connection": {"Upgrade"}, "Upgrade": {"tcp"}})
	if err != nil {
		return err
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if resp.StatusCode != http.StatusSwitchingProtocols || !ok {
		_ = resp.Body.Close()
		return Errorf(CodeFailed, "docker: exec did not start")
	}
	defer func() { _ = rwc.Close() }()
	if err := send(ExecOut{Ready: true}); err != nil {
		return err
	}

	// Output until the shell exits; input and resizes until then.
	outDone := make(chan struct{})
	var outErr error // set before outDone closes
	go func() {
		defer close(outDone)
		buf := make([]byte, 32<<10)
		for {
			n, err := rwc.Read(buf)
			if n > 0 {
				if outErr = send(ExecOut{Out: buf[:n]}); outErr != nil {
					return
				}
			}
			if err != nil {
				return // the shell exited
			}
		}
	}()
	for {
		select {
		case <-outDone:
			return outErr
		case <-ctx.Done():
			_ = rwc.Close()
			<-outDone
			return ctx.Err()
		case line, ok := <-in:
			if !ok {
				_ = rwc.Close()
				<-outDone
				return nil
			}
			var m ExecIn
			if err := decodeParams(line, &m); err != nil {
				continue // ignore a malformed line; the stream goes on
			}
			if len(m.In) > 0 {
				if _, err := rwc.Write(m.In); err != nil {
					<-outDone
					return nil
				}
			}
			if m.Resize != nil && validSize(m.Resize[0], m.Resize[1]) {
				q := url.Values{"w": {strconv.Itoa(m.Resize[0])}, "h": {strconv.Itoa(m.Resize[1])}}
				_, _ = d.do(ctx, http.MethodPost, execPath+"/resize", q) // the shell may be gone; output will tell
			}
		}
	}
}

// doBody makes one request with a JSON body and reads its answer.
func (d *Docker) doBody(ctx context.Context, method, path string, body any) ([]byte, error) {
	resp, err := d.sendBody(ctx, method, path, nil, body, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, Errorf(CodeFailed, "docker: reading the answer failed")
	}
	return b, nil
}
