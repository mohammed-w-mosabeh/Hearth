// Package safeexec is the only place in Hearth allowed to start processes
// (SECURITY S7). Commands are looked up by name in a static allowlist of
// absolute paths, every argument is validated, and no shell is ever involved.
package safeexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultTimeout bounds every command that does not set its own.
const DefaultTimeout = 10 * time.Second

// maxOutput caps captured stdout and stderr each.
const maxOutput = 1 << 20

var (
	ErrUnknownCommand = errors.New("safeexec: command not in allowlist")
	ErrBinary         = errors.New("safeexec: binary rejected")
)

// Spec describes one allowlisted command.
type Spec struct {
	// Paths are absolute candidates; the first that exists is used.
	Paths []string
	// Validate checks the complete argument list. It must reject anything
	// the command is not expected to receive.
	Validate func(args []string) error
	// Env lists variables passed through from hearthd's environment, on top
	// of the fixed PATH and LANG.
	Env []string
	// SetEnv are fixed KEY=value entries, set after Env, so neither the
	// environment nor anything the command reads can unset them.
	SetEnv []string
	// Timeout overrides DefaultTimeout when non-zero.
	Timeout time.Duration
}

// Result is the outcome of a finished command.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool
}

// Run executes the allowlisted command name with args.
func Run(ctx context.Context, name string, args ...string) (Result, error) {
	spec, ok := allowlist[name]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownCommand, name)
	}
	return run(ctx, spec, args)
}

// Check reports whether Run would accept name with args, without running
// anything.
func Check(name string, args ...string) error {
	spec, ok := allowlist[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownCommand, name)
	}
	return check(spec, args)
}

func check(spec Spec, args []string) error {
	for _, a := range args {
		if err := baseCheck(a); err != nil {
			return err
		}
	}
	if spec.Validate == nil {
		return fmt.Errorf("%w: spec has no validator", ErrInvalidArg)
	}
	return spec.Validate(args)
}

// Start starts the allowlisted command name and leaves it running: a program
// that becomes the user's browser has to outlive its caller, so it gets its
// own session, no timeout and no parent-death signal. It returns the
// command's failure if it ends within grace, nil if it is still running or
// ended well. Errors never carry args.
func Start(name string, grace time.Duration, args ...string) error {
	spec, ok := allowlist[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownCommand, name)
	}
	return start(spec, grace, args)
}

func start(spec Spec, grace time.Duration, args []string) error {
	if err := check(spec, args); err != nil {
		return errors.New("safeexec: arguments refused")
	}
	bin, err := resolve(spec.Paths)
	if err != nil {
		return err
	}
	// #nosec G204 -- bin comes from the static allowlist and args are validated above.
	cmd := exec.CommandContext(context.Background(), bin, args...) // never cancelled: it outlives us
	cmd.Env = buildEnv(spec.Env, spec.SetEnv)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("safeexec: cannot start %s", bin)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("safeexec: %s exited with %d", bin, cmd.ProcessState.ExitCode())
		}
	case <-time.After(grace):
	}
	return nil
}

func run(ctx context.Context, spec Spec, args []string) (Result, error) {
	if err := check(spec, args); err != nil {
		return Result{}, err
	}
	bin, err := resolve(spec.Paths)
	if err != nil {
		return Result{}, err
	}

	timeout := spec.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- bin comes from the static allowlist and args are validated above.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = buildEnv(spec.Env, spec.SetEnv)
	cmd.Dir = "/"
	cmd.Stdin = nil
	stdout := &capWriter{max: maxOutput}
	stderr := &capWriter{max: maxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		// Kill the whole process group, not just the direct child.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	err = cmd.Run()
	res := Result{
		Stdout:    stdout.buf.Bytes(),
		Stderr:    stderr.buf.Bytes(),
		ExitCode:  cmd.ProcessState.ExitCode(),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("safeexec: %s: %w", bin, ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return res, fmt.Errorf("safeexec: %s exited with %d: %w", bin, res.ExitCode, err)
	}
	if err != nil {
		return res, fmt.Errorf("safeexec: %s: %w", bin, err)
	}
	return res, nil
}

// resolve returns the first candidate that is a root-owned regular file not
// writable by group or others. A missing candidate is skipped; an unsafe one
// is an error, so a tampered binary is never silently replaced by another.
func resolve(paths []string) (string, error) {
	for _, p := range paths {
		fi, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("%w: %s: %w", ErrBinary, p, err)
		}
		if err := checkBinary(p, fi); err != nil {
			return "", err
		}
		return p, nil
	}
	return "", fmt.Errorf("%w: none of %v exists", ErrBinary, paths)
}

func checkBinary(p string, fi fs.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrBinary, p)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: %s is group- or world-writable", ErrBinary, p)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: %s: cannot read owner", ErrBinary, p)
	}
	if st.Uid != rootUID() {
		return fmt.Errorf("%w: %s is not owned by root", ErrBinary, p)
	}
	return nil
}

// rootUID is the uid that host root's files show as from this process. It is
// 0 normally, but inside a user namespace that does not map host root (e.g. a
// hardened systemd user unit, uid_map "1000 1000 1") they show as the
// kernel's overflow uid.
var rootUID = sync.OnceValue(func() uint32 {
	m, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return 0
	}
	if inside, ok := mapHostUID(string(m), 0); ok {
		return inside
	}
	return overflowUID()
})

// mapHostUID translates a host uid through uid_map lines
// "<inside> <outside> <count>".
func mapHostUID(uidMap string, host uint64) (uint32, bool) {
	for _, line := range strings.Split(uidMap, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		in, err1 := strconv.ParseUint(f[0], 10, 32)
		out, err2 := strconv.ParseUint(f[1], 10, 32)
		n, err3 := strconv.ParseUint(f[2], 10, 33)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if host >= out && host < out+n {
			return uint32(in + (host - out)), true // #nosec G115 -- bounded by the 32-bit uid_map ranges
		}
	}
	return 0, false
}

func overflowUID() uint32 {
	b, err := os.ReadFile("/proc/sys/kernel/overflowuid")
	if err == nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32); err == nil {
			return uint32(v)
		}
	}
	return 65534
}

func buildEnv(pass, set []string) []string {
	env := []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	for _, k := range pass {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, set...)
}

type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room < len(p) {
		w.buf.Write(p[:max(room, 0)])
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}
