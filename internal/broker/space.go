package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ActionSpaceClean is the polkit action for the Space advisor's cleanups.
const ActionSpaceClean = "org.hearth.space.clean"

// AptArchives is where apt keeps the packages it downloaded.
const AptArchives = "/var/cache/apt/archives"

// JournalKeep lists the sizes journal.vacuum may keep, in MiB.
var JournalKeep = []int{100, 200, 500, 1000}

// JournalParams are journal.vacuum's params.
type JournalParams struct {
	KeepMiB int `json:"keep_mib"`
}

// CleanResult is what apt.clean freed.
type CleanResult struct {
	Removed int   `json:"removed"`
	Freed   int64 `json:"freed"`
}

// SpaceMethods are the broker's cleanups (SECURITY S16c). apt.clean is not
// apt-get clean: that also opens partial/, which belongs to _apt and which
// a broker without capabilities cannot read. It holds apt's own lock and
// removes the downloaded .deb files, nothing else.
func SpaceMethods(run Runner, archives string) []Method {
	keep := func(p json.RawMessage) (string, error) {
		var v JournalParams
		if err := decodeParams(p, &v); err != nil {
			return "", err
		}
		for _, k := range JournalKeep {
			if v.KeepMiB == k {
				return "--vacuum-size=" + strconv.Itoa(k) + "M", nil
			}
		}
		return "", badParam("keep_mib must be 100, 200, 500 or 1000")
	}
	return []Method{{
		Name: "apt.clean", Privileged: true, Action: ActionSpaceClean, Timeout: time.Minute,
		Check: func(_ context.Context, p json.RawMessage) error { return noParams(p) },
		Handle: func(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
			if err := noParams(p); err != nil {
				return nil, err
			}
			return cleanArchives(archives)
		},
	}, {
		Name: "journal.vacuum", Privileged: true, Action: ActionSpaceClean, Timeout: 2 * time.Minute,
		Check: func(_ context.Context, p json.RawMessage) error { _, err := keep(p); return err },
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			arg, err := keep(p)
			if err != nil {
				return nil, err
			}
			res, err := run(ctx, "journal-vacuum", arg)
			if err != nil || res.ExitCode != 0 {
				msg := strings.TrimSpace(string(res.Stderr))
				if msg == "" && err != nil {
					msg = err.Error()
				}
				return nil, Errorf(CodeFailed, "journalctl: %s", clipMsg(msg))
			}
			return map[string]bool{"done": true}, nil
		},
	}}
}

// cleanArchives removes dir's .deb files while holding apt's lock, so it
// never takes a package from under a running apt.
func cleanArchives(dir string) (CleanResult, error) {
	var out CleanResult
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- a fixed root-owned path
	if err != nil {
		return out, Errorf(CodeFailed, "apt's lock: %s", clipMsg(err.Error()))
	}
	defer func() { _ = lock.Close() }() // also releases the lock
	fl := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(lock.Fd(), syscall.F_SETLK, &fl); err != nil {
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES) {
			return out, Errorf(CodeConflict, "apt is running")
		}
		return out, Errorf(CodeFailed, "apt's lock: %s", clipMsg(err.Error()))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, Errorf(CodeFailed, "%s", clipMsg(err.Error()))
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".deb") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return out, Errorf(CodeFailed, "%s", clipMsg(err.Error()))
		}
		out.Removed++
		out.Freed += fi.Size()
	}
	return out, nil
}
