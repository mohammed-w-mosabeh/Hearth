package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/pathcheck"
)

// Path checks (ROADMAP 9.3, SECURITY S12b). The broker never resolves a
// bind itself: it cannot see home, and gains no capability to. It asks
// hearth-pathcheck@.service, through PathChecker, before polkit and again
// before up, and refuses the deploy unless both answers are OK and equal.
// After up, VerifyLive compares each container's mounts with the answer.

// PathChecker asks hearth-pathcheck.socket.
type PathChecker struct {
	Path    string        // default pathcheck.SocketPath
	Timeout time.Duration // default 20 s
}

// ErrPathCheck means the path check could not be asked; the deploy stops.
var ErrPathCheck = errors.New("the path check is not available")

// Check sends one spec and returns the answer.
func (p PathChecker) Check(ctx context.Context, spec json.RawMessage) (pathcheck.Reply, error) {
	path, timeout := p.Path, p.Timeout
	if path == "" {
		path = pathcheck.SocketPath
	}
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return pathcheck.Reply{}, fmt.Errorf("%w: %w", ErrPathCheck, err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := json.NewEncoder(conn).Encode(pathcheck.Request{Spec: spec}); err != nil {
		return pathcheck.Reply{}, fmt.Errorf("%w: %w", ErrPathCheck, err)
	}
	line, err := bufio.NewReaderSize(io.LimitReader(conn, 1<<20), 64<<10).ReadBytes('\n')
	if err != nil {
		return pathcheck.Reply{}, fmt.Errorf("%w: %w", ErrPathCheck, err)
	}
	var r pathcheck.Reply
	if err := json.Unmarshal(line, &r); err != nil {
		return pathcheck.Reply{}, fmt.Errorf("%w: %w", ErrPathCheck, err)
	}
	if r.OK == (len(r.Refused) != 0) {
		return pathcheck.Reply{}, fmt.Errorf("%w: an inconsistent answer", ErrPathCheck)
	}
	return r, nil
}

// Recheck is the check before up: the answer must be OK and resolve every
// root and bind exactly as first, or a link moved in between.
func (p PathChecker) Recheck(ctx context.Context, spec json.RawMessage, first pathcheck.Reply) (pathcheck.Reply, error) {
	r, err := p.Check(ctx, spec)
	switch {
	case err != nil:
		return r, err
	case !r.OK:
		return r, nil
	case !pathcheck.Same(first, r):
		return pathcheck.Reply{Refused: []string{"a bind source resolved differently between the two checks"}}, nil
	}
	return r, nil
}

// VerifyLive reads the mounts of one running container of service (its
// process pid, from the Engine) and returns the binds whose live mount is
// not the checked one. procRoot is "/proc" outside tests.
func VerifyLive(procRoot string, pid int, service string, r pathcheck.Reply) ([]string, error) {
	if pid <= 0 {
		return nil, errors.New("the container is not running")
	}
	var binds []pathcheck.Resolved
	for _, b := range r.Binds {
		if b.Service == service {
			binds = append(binds, b)
		}
	}
	if len(binds) == 0 {
		return nil, nil
	}
	f, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "mountinfo")) // #nosec G304 -- fixed /proc layout, pid a positive number
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return nil, err
	}
	return pathcheck.VerifyMounts(b, binds), nil
}
